export default function ToggleIcon({
  strokeWidth = 1.5,
}: {
  strokeWidth?: number;
}) {
  return (
    <svg
      width="13"
      height="14"
      viewBox="0 0 13 14"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M1.58317 3.66667L4.24983 1L6.9165 3.66667M6.08349 10.3333L8.75016 13L11.4168 10.3333M4.24983 1.66667V7.33333M8.75016 6.66667V12.3333"
        stroke="#F9F9F9"
        strokeOpacity="0.6"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
