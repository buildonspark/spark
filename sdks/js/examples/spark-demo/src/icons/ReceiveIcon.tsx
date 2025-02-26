export default function ReceiveIcon({
  stroke = "#FAFAFA",
  strokeWidth = "2",
}: {
  stroke?: string;
  strokeWidth?: string;
}) {
  return (
    <svg
      width="32"
      height="33"
      viewBox="0 0 32 33"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M20.34 24.8333H7.66683M7.66683 24.8333V10.8389M7.66683 24.8333L23.3335 9.16665"
        stroke={stroke}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
