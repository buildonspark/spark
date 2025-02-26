export default function SendIcon({
  stroke = "#F9F9F9",
  strokeWidth = "2",
}: {
  stroke?: string;
  strokeWidth?: string;
}) {
  return (
    <svg
      width="32"
      height="32"
      viewBox="0 0 32 32"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M23.3332 8.66669L8.6665 23.3334M11.66 7.66669H22.9998C23.7362 7.66669 24.3332 8.26364 24.3332 9.00002V21.6611"
        stroke={stroke}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
