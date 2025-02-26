export default function BitcoinIcon({
  strokeWidth = "2.75",
}: {
  strokeWidth?: string;
}) {
  return (
    <svg
      width="18"
      height="26"
      viewBox="0 0 18 26"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M4.85004 5.29999V13M4.85004 5.29999H12C14.1263 5.29999 15.85 7.02368 15.85 9.14998C15.85 11.2763 14.1263 13 12 13M4.85004 5.29999H1.55005M4.85004 13V20.6999M4.85004 13H12M12 13C14.1263 13 15.85 14.7237 15.85 16.85C15.85 18.9762 14.1263 20.6999 12 20.6999H4.85004M4.85004 20.6999H1.55005M9.25002 2V5.29999M9.25002 20.6999V23.9999"
        stroke="white"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
