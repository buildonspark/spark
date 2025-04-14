export default function ArrowLeft({
  strokeWidth = "1.5",
}: {
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
        d="M10 18.25L3.75 12M3.75 12L10 5.75M3.75 12H20.25"
        stroke="#F9F9F9"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
