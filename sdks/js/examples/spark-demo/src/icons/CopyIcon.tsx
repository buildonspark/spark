export default function CopyIcon({
  stroke = "#F9F9F9",
  strokeWidth = "1.5",
}: {
  stroke?: string;
  strokeWidth?: string;
}) {
  return (
    <svg
      width="24"
      height="24"
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M7.75 7.75V3.75H20.25V16.26H16.25M16.25 7.75V20.25H3.75V7.75H16.25Z"
        stroke={stroke}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
