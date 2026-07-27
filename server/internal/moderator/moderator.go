// Package moderator periodically re-checks indexed torrents with an
// OpenAI-compatible chat model and removes adult and spam listings that the
// static keyword filter let through.
//
// The pass is incremental: every torrent carries a reviewed_at stamp, so each
// run only pays for rows it has never classified. Removed infohashes go on a
// blocklist so the crawler cannot re-add them.
package moderator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"dhtsearch/server/internal/store"
)

// Labels returned by the classifier.
const (
	labelOK    = "ok"
	labelAdult = "adult"
	labelSpam  = "spam"
)

// Config configures the moderation pass.
type Config struct {
	BaseURL    string        // OpenAI-compatible base, e.g. https://api.deepseek.com/v1
	APIKey     string        // never logged
	Model      string        // e.g. deepseek-v4-flash
	Interval   time.Duration // how often to sweep
	BatchSize  int           // titles per request
	MaxBatches int           // batches per sweep; 0 = unlimited
	Timeout    time.Duration // per-request timeout
	DryRun     bool          // classify and log, but delete nothing
	TrimTitles bool          // also strip advertising from titles
	Logger     *log.Logger
	HTTPClient *http.Client // optional; for tests
}

// Moderator runs the periodic classification pass.
type Moderator struct {
	st  *store.Store
	cfg Config
	hc  *http.Client
}

// Summary reports what a single sweep did.
type Summary struct {
	Reviewed int
	Adult    int
	Spam     int
	Deleted  int64
	Trimmed  int64
}

// New validates cfg and builds a Moderator.
func New(st *store.Store, cfg Config) (*Moderator, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("moderator: API key is empty")
	}
	if cfg.Model == "" {
		return nil, errors.New("moderator: model is empty")
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("moderator: base URL is empty")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Moderator{st: st, cfg: cfg, hc: hc}, nil
}

// Run sweeps every Interval until ctx is cancelled. It blocks.
func (m *Moderator) Run(ctx context.Context) {
	m.cfg.Logger.Printf("moderator: enabled (model=%s interval=%s batch=%d dry-run=%v trim-titles=%v)",
		m.cfg.Model, m.cfg.Interval, m.cfg.BatchSize, m.cfg.DryRun, m.cfg.TrimTitles)
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s, err := m.SweepOnce(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				m.cfg.Logger.Printf("moderator: sweep: %v", err)
				m.st.IncrStat("llm_errors", 1)
				continue
			}
			if s.Reviewed > 0 {
				m.cfg.Logger.Printf("moderator: reviewed=%d adult=%d spam=%d deleted=%d trimmed=%d",
					s.Reviewed, s.Adult, s.Spam, s.Deleted, s.Trimmed)
			}
		}
	}
}

// SweepOnce classifies up to MaxBatches batches of unreviewed torrents.
// A batch that fails is left unmarked so the next sweep retries it.
func (m *Moderator) SweepOnce(ctx context.Context) (Summary, error) {
	var total Summary
	for n := 0; m.cfg.MaxBatches == 0 || n < m.cfg.MaxBatches; n++ {
		cands, err := m.st.Unreviewed(m.cfg.BatchSize)
		if err != nil {
			return total, fmt.Errorf("load candidates: %w", err)
		}
		if len(cands) == 0 {
			return total, nil
		}
		s, err := m.reviewBatch(ctx, cands)
		total.Reviewed += s.Reviewed
		total.Adult += s.Adult
		total.Spam += s.Spam
		total.Deleted += s.Deleted
		total.Trimmed += s.Trimmed
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// reviewBatch classifies one batch and applies the verdicts.
func (m *Moderator) reviewBatch(ctx context.Context, cands []store.Candidate) (Summary, error) {
	var s Summary
	verdicts, err := m.classify(ctx, cands)
	if err != nil {
		return s, err
	}

	now := time.Now().Unix()
	var adultH, adultN, spamH, spamN []string
	trims := map[string]string{}
	for i, c := range cands {
		switch verdicts[i].label {
		case labelAdult:
			adultH, adultN = append(adultH, c.InfoHash), append(adultN, c.Name)
		case labelSpam:
			spamH, spamN = append(spamH, c.InfoHash), append(spamN, c.Name)
		default:
			// Only worth trimming titles that survive moderation.
			if v := verdicts[i].clean; v != "" {
				trims[c.InfoHash] = v
			}
		}
	}
	s.Reviewed, s.Adult, s.Spam = len(cands), len(adultH), len(spamH)

	// Trims are logged before/after either way — that log is the audit trail
	// for what the model rewrote.
	raw := make(map[string]string, len(cands))
	for _, c := range cands {
		raw[c.InfoHash] = c.Name
	}
	for h, clean := range trims {
		m.cfg.Logger.Printf("moderator: trim %s %q -> %q", h, raw[h], clean)
	}

	if m.cfg.DryRun {
		for i, h := range adultH {
			m.cfg.Logger.Printf("moderator: [dry-run] would remove adult %s %q", h, adultN[i])
		}
		for i, h := range spamH {
			m.cfg.Logger.Printf("moderator: [dry-run] would remove spam %s %q", h, spamN[i])
		}
	} else {
		n, err := m.st.SetCleanNames(trims)
		if err != nil {
			return s, fmt.Errorf("set clean names: %w", err)
		}
		s.Trimmed = n
		m.st.IncrStat("llm_titles_trimmed", n)
		for _, g := range []struct {
			hashes, names []string
			reason, stat  string
		}{
			{adultH, adultN, labelAdult, "llm_adult_removed"},
			{spamH, spamN, labelSpam, "llm_spam_removed"},
		} {
			if len(g.hashes) == 0 {
				continue
			}
			n, err := m.st.Block(g.hashes, g.names, g.reason, now)
			if err != nil {
				return s, fmt.Errorf("block %s: %w", g.reason, err)
			}
			s.Deleted += n
			m.st.IncrStat(g.stat, n)
			for i, h := range g.hashes {
				m.cfg.Logger.Printf("moderator: removed %s %s %q", g.reason, h, g.names[i])
			}
		}
	}

	// Mark the whole batch reviewed. Rows just deleted simply match nothing.
	hashes := make([]string, len(cands))
	for i, c := range cands {
		hashes[i] = c.InfoHash
	}
	if err := m.st.MarkReviewed(hashes, now); err != nil {
		return s, fmt.Errorf("mark reviewed: %w", err)
	}
	m.st.IncrStat("llm_reviewed", int64(len(cands)))
	return s, nil
}

// systemPromptFor builds the instructions. The title-trimming half is left out
// entirely when the feature is off, so it costs no tokens.
func systemPromptFor(trim bool) string {
	if trim {
		return promptHead + "For each one, assign a label and return a cleaned-up title.\n" +
			promptLabel + promptClean + promptTailTrim
	}
	return promptHead + "Assign each one a label.\n" + promptLabel + promptTail
}

const promptHead = `You are a content-moderation classifier for a BitTorrent metadata index.
You receive a JSON array of torrent listings, each with "i" (its number),
"title", "size" and "files". Judge only by "title"; "size" and "files" are
context. `

const promptLabel = `
LABEL — assign each listing exactly one:

- "adult": pornography or sexually explicit material (including JAV, hentai,
  camgirl/webcam rips, escort or sex-work advertising).
- "spam": worthless or malicious listings — malware and crack bait, keyword-stuffed
  junk, "free download" ad shells, phishing, scam uploads, link-farm torrents,
  or listings whose title is gibberish.
- "ok": everything else.

Rules:
- Mainstream movies, TV, music, games, software, books, courses and anime are "ok"
  even when the title is lowbrow, violent, horror, or in a non-English language.
- Copyright infringement alone is NOT spam. Almost every listing here is a pirated
  release; that by itself never justifies a non-"ok" label.
- Sex education, documentaries and mainstream films with sexual themes are "ok";
  reserve "adult" for material whose purpose is explicit sexual content.
- When genuinely unsure, answer "ok".
`

const promptClean = `
CLEAN — the same title with advertising removed. Copy the title and DELETE the
promotional spans. Never reword, translate, reorder, re-space or re-punctuate
anything you keep: the result must be the original title with characters removed
and nothing else.

Delete:
- Site and forum banners, in any bracket style: 【高清剧集网发布 www.xxx.com】,
  [ www.UsaBit.com ] -, (www.xxx.net), www.xxx.com@, 【xxx论坛】.
- Bare URLs, domains and invite codes that advertise a site.
- Solicitations: 地址发布页, 收藏不迷路, 关注我们, 更多资源请访问, 最新地址,
  免费下载, "visit", "download more at", QQ/WeChat/Telegram handles and numbers.
- Uploader ad tags appended to the end: [TGx], [EZTVx.to], -RARBG.com, [rartv].

Keep everything that describes the release, even if it looks like noise:
- Title, year, season/episode, resolution, source, codec, bit depth, HDR.
- Audio format and channels, language and subtitle tags, file extension.
- Release-group and fansub tags: -FraMeSToR, [Erai-raws], [ReinForce], -NTb.
  These identify the release, they are not advertising.

If a title has no advertising, return it unchanged. Never return an empty
string, and never delete so much that the work is no longer identifiable.
"clean" holds the title text ONLY — never append the size or the file count.
`

const promptTailTrim = `
Respond with JSON only, no prose, in exactly this shape:
{"verdicts":[{"i":1,"label":"ok","clean":"cleaned title"},{"i":2,"label":"adult","clean":"cleaned title"}]}
Include exactly one entry for every input number.`

const promptTail = `
Respond with JSON only, no prose, in exactly this shape:
{"verdicts":[{"i":1,"label":"ok"},{"i":2,"label":"adult"}]}
Include exactly one entry for every input number.`

// verdict is the model's answer for one candidate. clean is empty unless the
// model proposed a trimmed title that passed validation.
type verdict struct {
	label string
	clean string
}

// classify returns a verdict per candidate, aligned by index. Entries the model
// omits or labels unknown default to "ok" (fail-open: never delete on doubt).
func (m *Moderator) classify(ctx context.Context, cands []store.Candidate) ([]verdict, error) {
	// The listing goes over as JSON so "title" is unambiguously delimited. With
	// a "<title> [1.2GB, 3 files]" line the model intermittently copied the
	// size suffix into the cleaned title, which validation then rejected.
	items := make([]promptItem, len(cands))
	for i, c := range cands {
		name, _ := promptName(c.Name)
		items[i] = promptItem{I: i + 1, Title: name, Size: humanSize(c.TotalSize), Files: c.FileCount}
	}
	listing, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(chatRequest{
		Model:          m.cfg.Model,
		Temperature:    0,
		ResponseFormat: &responseFormat{Type: "json_object"},
		Messages: []chatMessage{
			{Role: "system", Content: systemPromptFor(m.cfg.TrimTitles)},
			{Role: "user", Content: string(listing)},
		},
	})
	if err != nil {
		return nil, err
	}

	raw, err := m.post(ctx, reqBody)
	if err != nil {
		return nil, err
	}

	out := make([]verdict, len(cands))
	for i := range out {
		out[i] = verdict{label: labelOK}
	}
	var parsed verdictResponse
	if err := json.Unmarshal([]byte(stripFences(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("parse verdicts: %w (content=%.200q)", err, raw)
	}
	for _, v := range parsed.Verdicts {
		if v.I < 1 || v.I > len(cands) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v.Label)) {
		case labelAdult:
			out[v.I-1].label = labelAdult
		case labelSpam:
			out[v.I-1].label = labelSpam
		}
		if m.cfg.TrimTitles {
			out[v.I-1].clean = cleanTitle(cands[v.I-1].Name, v.Clean)
		}
	}
	return out, nil
}

// post sends one chat completion request, retrying on 429 and 5xx.
func (m *Moderator) post(ctx context.Context, body []byte) (string, error) {
	url := strings.TrimSuffix(m.cfg.BaseURL, "/") + "/chat/completions"
	const attempts = 3
	var lastErr error
	for a := 0; a < attempts; a++ {
		if a > 0 {
			delay := time.Duration(1<<uint(a)) * time.Second
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)

		resp, err := m.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = err
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("chat completions: http %d: %.200s", resp.StatusCode, payload)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			// 4xx other than rate limiting is not retryable (bad key, bad model).
			return "", fmt.Errorf("chat completions: http %d: %.200s", resp.StatusCode, payload)
		}
		var cr chatResponse
		if err := json.Unmarshal(payload, &cr); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		if len(cr.Choices) == 0 {
			return "", errors.New("chat completions: no choices in response")
		}
		return cr.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("chat completions: %d attempts failed: %w", attempts, lastErr)
}

// stripFences removes a ```json ... ``` wrapper if the model added one.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// promptNameCap bounds how much of a title reaches the model, so one absurd
// name cannot blow up the prompt.
const promptNameCap = 200

// sanitize keeps a title on one line so it cannot break the numbered list.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

// promptName renders a title for the prompt, reporting whether it had to be cut
// short. A truncated title must never be trimmed: the model never saw the tail,
// so applying its answer would silently delete the rest of the name.
func promptName(s string) (text string, truncated bool) {
	s = sanitize(s)
	if r := []rune(s); len(r) > promptNameCap {
		return string(r[:promptNameCap]) + "…", true
	}
	return s, false
}

// Guards against a model that rewrites instead of trimming. A cleaned title
// must keep at least this many runes, and this share of the original.
const (
	minCleanRunes = 4
	minCleanRatio = 0.25
)

// cleanTitle validates a model-proposed title against the original and returns
// the value to store, or "" when the proposal must be ignored.
//
// The model is told to only delete characters, so the proposal must remain a
// subsequence of the original. That single check rejects rewrites,
// translations, re-spacing and outright hallucination — anything that invents a
// rune the original did not have, in the order it had it.
func cleanTitle(raw, proposed string) string {
	full, truncated := promptName(raw)
	if truncated {
		return ""
	}
	proposed = strings.Trim(collapseSpaces(proposed), " \t-_|")
	if proposed == "" || proposed == full || proposed == collapseSpaces(full) {
		return ""
	}
	rp, rf := []rune(proposed), []rune(full)
	if len(rp) > len(rf) ||
		len(rp) < minCleanRunes ||
		float64(len(rp)) < minCleanRatio*float64(len(rf)) {
		return ""
	}
	if !isSubsequence(rp, rf) {
		return ""
	}
	return proposed
}

// isSubsequence reports whether sub can be obtained from of by deleting runes.
func isSubsequence(sub, of []rune) bool {
	j := 0
	for _, c := range sub {
		for j < len(of) && of[j] != c {
			j++
		}
		if j == len(of) {
			return false
		}
		j++
	}
	return true
}

// collapseSpaces squeezes runs of whitespace into a single space, tidying the
// gap left where an advertising span was cut out.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// --- OpenAI-compatible wire types ---

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// promptItem is one listing as presented to the model.
type promptItem struct {
	I     int    `json:"i"`
	Title string `json:"title"`
	Size  string `json:"size"`
	Files int    `json:"files"`
}

type verdictResponse struct {
	Verdicts []struct {
		I     int    `json:"i"`
		Label string `json:"label"`
		Clean string `json:"clean"`
	} `json:"verdicts"`
}
