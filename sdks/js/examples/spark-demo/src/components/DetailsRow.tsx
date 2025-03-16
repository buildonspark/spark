type DetailsRowProps = {
  borderTop?: boolean;
  title?: string;
  subtitle?: string | null;
  logoRight?: React.ReactNode;
  logoLeft?: React.ReactNode;
  logoLeftCircleBackground?: boolean;
  logoRightMargin?: number;
  onClick?: () => void;
};

export default function DetailsRow({
  title,
  subtitle,
  logoRight,
  logoLeft,
  logoLeftCircleBackground = false,
  logoRightMargin = 16,
  borderTop = false,
  onClick,
}: DetailsRowProps) {
  return (
    <div
      className={`flex h-[72px] flex-row items-center justify-between ${
        borderTop ? "border-t border-[#2d3845]" : ""
      } ${onClick ? "cursor-pointer" : ""}`}
      onClick={onClick}
    >
      <div className="flex flex-row items-center">
        {logoLeft && (
          <div
            className={`flex h-10 w-10 items-center justify-center ${
              logoLeftCircleBackground
                ? "mr-2 rounded-full border border-[#10151C]"
                : ""
            } `}
            style={
              logoLeftCircleBackground
                ? {
                    borderWidth: "0.33px",
                    borderColor: "rgba(249, 249, 249, 0.25)",
                    background:
                      "linear-gradient(0deg, rgba(255, 255, 255, 0.02) 0%, rgba(255, 255, 255, 0.02) 100%), linear-gradient(180deg, #10151C 0%, #10151C 11.79%, #11161D 21.38%, #11161E 29.12%, #11171E 35.34%, #11171F 40.37%, #12171F 44.56%, #121820 48.24%, #121820 51.76%, #131921 55.44%, #131921 59.63%, #131922 64.66%, #131922 70.88%, #131A22 78.62%, #141A22 88.21%, #141A22 100%)",
                  }
                : {}
            }
          >
            {logoLeft}
          </div>
        )}
        <div
          className={`flex max-w-[190px] flex-col justify-between ${!logoLeft ? "ml-4" : ""}`}
        >
          {title && (
            <div className="overflow-hidden text-ellipsis whitespace-nowrap text-[12px] text-[#f9f9f9]">
              {title}
            </div>
          )}
          {subtitle && (
            <div className="overflow-hidden text-ellipsis whitespace-nowrap text-[12px] text-[#f9f9f9] opacity-50">
              {subtitle}
            </div>
          )}
        </div>
      </div>
      {logoRight && (
        <div className={`mr-4 flex flex-col items-center justify-between`}>
          {logoRight}
        </div>
      )}
    </div>
  );
}
