type DetailsRowProps = {
  borderTop?: boolean;
  title?: string;
  subtitle?: string;
  logoRight?: React.ReactNode;
  logoLeft?: React.ReactNode;
  logoLeftCircleBackground?: boolean;
  onClick?: () => void;
};

export default function DetailsRow({
  title,
  subtitle,
  logoRight,
  logoLeft,
  logoLeftCircleBackground = false,
  borderTop = false,
  onClick,
}: DetailsRowProps) {
  console.log(logoLeftCircleBackground);
  return (
    <div
      className={`h-[72px] flex flex-row items-center justify-between ${
        borderTop ? "border-t border-[#2d3845]" : ""
      }`}
      onClick={onClick}
    >
      <div className="flex flex-row items-center">
        {logoLeft && (
          <div
            className={`flex items-center justify-center w-10 h-10 ${
              logoLeftCircleBackground
                ? "border border-[#10151C] rounded-full mr-2"
                : ""
            }`}
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
          className={`flex flex-col justify-between max-w-[290px] ${
            !logoLeft ? "ml-4" : ""
          }`}
        >
          {title && (
            <div className="text-[12px] text-[#f9f9f9] overflow-hidden text-ellipsis whitespace-nowrap ">
              {title}
            </div>
          )}
          {subtitle && (
            <div className="text-[12px] text-[#f9f9f9] opacity-50 overflow-hidden text-ellipsis whitespace-nowrap ">
              {subtitle}
            </div>
          )}
        </div>
      </div>
      {logoRight && (
        <div className="flex flex-col items-center justify-between pr-4">
          {logoRight}
        </div>
      )}
    </div>
  );
}
