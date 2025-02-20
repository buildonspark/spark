export default function CloseIcon({
  strokeWidth = "1.5",
}: {
  strokeWidth?: string;
}) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M0.75 0.75L15.25 15.25M15.25 0.75L0.75 15.25"
        stroke="#F9F9F9"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
      />
    </svg>
  );
}
