export default function SparkIcon({
  opacity = 1,
  width = 21,
  height = 20,
  rotation = 0,
  fill = "#F9F9F9",
  style = {},
}: {
  opacity?: number;
  width?: number;
  height?: number;
  rotation?: number;
  fill?: string;
  style?: React.CSSProperties;
}) {
  return (
    <svg
      width={`${width}px`}
      height={`${height}px`}
      viewBox="0 0 21 20"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      style={{
        opacity: opacity,
        transform: `rotate(${rotation}deg)`,
        ...style,
      }}
    >
      <path
        d="M12.7782 0.327462L12.5842 7.53494L19.3913 5.16801L20.6482 9.0792L13.7366 11.121L18.1174 16.902L14.7333 19.2287L10.6822 13.3251L6.56957 19.2368L3.24752 16.8199L7.6421 11.1013L0.744369 9.01523L2.02493 5.11175L8.81759 7.5228L8.67006 0.319982L12.7782 0.327462Z"
        fill={fill}
      />
    </svg>
  );
}
