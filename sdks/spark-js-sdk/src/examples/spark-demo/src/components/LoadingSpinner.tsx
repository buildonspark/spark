import styled from "styled-components";

type LoadingProps = { size: number };

export const LoadingSpinner = (props: LoadingProps) => {
  return (
    <Rotate size={props.size}>
      <LoadingSvg />
    </Rotate>
  );
};

const Rotate = styled.div<LoadingProps>`
  display: inline-flex;
  animation: rotate 1s linear infinite;
  width: ${(props) => `${props.size}px`};
  height: ${(props) => `${props.size}px`};

  @keyframes rotate {
    0% {
      transform: rotate(0deg);
    }
    100% {
      transform: rotate(360deg);
    }
  }
`;

const LoadingSvg = () => (
  <svg
    width="100%"
    viewBox="0 0 12 12"
    fill="none"
    xmlns="http://www.w3.org/2000/svg"
  >
    <g clipPath="url(#clip0_1200_7026)">
      <circle cx="6" cy="6" r="5.25" stroke="#333333" strokeWidth="1.5" />
      <path
        d="M6 11.25C3.10051 11.25 0.75 8.89949 0.75 6C0.75 3.10051 3.10051 0.75 6 0.75"
        stroke="black"
        strokeWidth="1.5"
        strokeLinecap="round"
      />
      <mask
        id="mask0_1200_7026"
        style={{ maskType: "alpha" }}
        maskUnits="userSpaceOnUse"
        x="0"
        y="0"
        width="7"
        height="12"
      >
        <path
          d="M6 11.25C3.10051 11.25 0.75 8.89949 0.75 6C0.75 3.10051 3.10051 0.75 6 0.75"
          stroke="black"
          strokeWidth="1.5"
          strokeLinecap="round"
        />
      </mask>
      <g mask="url(#mask0_1200_7026)">
        <g filter="url(#filter0_f_1200_7026)">
          <ellipse
            cx="6.375"
            cy="2.625"
            rx="35.25"
            ry="14.25"
            fill="url(#paint0_radial_1200_7026)"
          />
        </g>
        <g filter="url(#filter1_f_1200_7026)">
          <ellipse
            cx="6.75"
            cy="-5.25"
            rx="23.625"
            ry="19.125"
            fill="url(#paint1_radial_1200_7026)"
          />
        </g>
      </g>
    </g>
    <defs>
      <filter
        id="filter0_f_1200_7026"
        x="-33.375"
        y="-16.125"
        width="79.5"
        height="37.5"
        filterUnits="userSpaceOnUse"
        colorInterpolationFilters="sRGB"
      >
        <feFlood floodOpacity="0" result="BackgroundImageFix" />
        <feBlend
          mode="normal"
          in="SourceGraphic"
          in2="BackgroundImageFix"
          result="shape"
        />
        <feGaussianBlur
          stdDeviation="2.25"
          result="effect1_foregroundBlur_1200_7026"
        />
      </filter>
      <filter
        id="filter1_f_1200_7026"
        x="-21.375"
        y="-28.875"
        width="56.25"
        height="47.25"
        filterUnits="userSpaceOnUse"
        colorInterpolationFilters="sRGB"
      >
        <feFlood floodOpacity="0" result="BackgroundImageFix" />
        <feBlend
          mode="normal"
          in="SourceGraphic"
          in2="BackgroundImageFix"
          result="shape"
        />
        <feGaussianBlur
          stdDeviation="2.25"
          result="effect1_foregroundBlur_1200_7026"
        />
      </filter>
      <radialGradient
        id="paint0_radial_1200_7026"
        cx="0"
        cy="0"
        r="1"
        gradientUnits="userSpaceOnUse"
        gradientTransform="translate(6.44679 2.59598) rotate(161.271) scale(23.5 14.7413)"
      >
        <stop stopColor="#0066FF" />
        <stop offset="1" stopColor="#0066FF" stopOpacity="0" />
      </radialGradient>
      <radialGradient
        id="paint1_radial_1200_7026"
        cx="0"
        cy="0"
        r="1"
        gradientUnits="userSpaceOnUse"
        gradientTransform="translate(6.79812 -5.28895) rotate(145.825) scale(18.0291 17.2834)"
      >
        <stop stopColor="#FFC700" />
        <stop offset="1" stopColor="#BF09FF" stopOpacity="0" />
      </radialGradient>
      <clipPath id="clip0_1200_7026">
        <rect width="12" height="12" fill="white" />
      </clipPath>
    </defs>
  </svg>
);
