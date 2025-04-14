interface ChevronIconProps {
  direction?: "right" | "left" | "up" | "down";
  stroke?: string;
  strokeWidth?: number;
  opacity?: number;
  height?: number;
  width?: number;
}
export default function ChevronIcon({
  direction = "right",
  stroke = "#F9F9F9",
  strokeWidth = 1.5,
  opacity = 0.5,
  height = 24,
  width = 24,
}: ChevronIconProps) {
  const paths = {
    right: "M10 16L14 12L10 8",
    left: "M14.6667 18.0001L9.33342 12.6667L14.6667 7.33342",
    up: "M8 14L12 10L16 14",
    down: "M8 10L12 14L16 10",
  };

  return (
    <svg
      width={width}
      height={height}
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
    >
      <g opacity={opacity}>
        <path
          d={paths[direction]}
          stroke={stroke}
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </g>
    </svg>
  );
}
