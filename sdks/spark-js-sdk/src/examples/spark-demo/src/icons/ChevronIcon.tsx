interface ChevronIconProps {
  direction?: "right" | "left" | "up" | "down";
}
export default function ChevronIcon({ direction = "right" }: ChevronIconProps) {
  const paths = {
    right: "M10 16L14 12L10 8",
    left: "M14 16L10 12L14 8",
    up: "M8 14L12 10L16 14",
    down: "M8 10L12 14L16 10",
  };

  return (
    <svg
      width="24"
      height="24"
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <g opacity="0.5">
        <path
          d={paths[direction]}
          stroke="#F9F9F9"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </g>
    </svg>
  );
}
