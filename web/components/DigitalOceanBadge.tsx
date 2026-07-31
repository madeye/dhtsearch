import Image from "next/image";

export default function DigitalOceanBadge({
  className = "",
}: {
  className?: string;
}) {
  return (
    <a
      href="https://www.digitalocean.com/?refcode=b7f5848a6ce2&utm_campaign=Referral_Invite&utm_medium=Referral_Program&utm_source=badge"
      target="_blank"
      rel="noopener noreferrer"
      className={`inline-block ${className}`}
    >
      <Image
        src="/digitalocean-badge.svg"
        alt="DigitalOcean Referral Badge"
        width={200}
        height={65}
        className="h-10 w-auto"
      />
    </a>
  );
}
