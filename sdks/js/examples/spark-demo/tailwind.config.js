/** @type {import('tailwindcss').Config} */
const config = {
  content: ["./src/**/*.{js,ts,jsx,tsx}"],
  theme: {
    extend: {
      fontFamily: {
        inter: ["var(--font-inter)", "sans-serif"],
        sans: ["var(--font-inter)", "sans-serif"],
      },
      colors: {
        black: {
          DEFAULT: "#0A0A0A",
          0: "rgba(10, 10, 10, 0.00)",
          4: "rgba(10, 10, 10, 0.04)",
          6: "rgba(10, 10, 10, 0.06)",
          8: "rgba(10, 10, 10, 0.08)",
          10: "rgba(10, 10, 10, 0.10)",
          12: "rgba(10, 10, 10, 0.12)",
          20: "rgba(10, 10, 10, 0.20)",
          30: "rgba(10, 10, 10, 0.30)",
          40: "rgba(10, 10, 10, 0.40)",
          50: "rgba(10, 10, 10, 0.50)",
          60: "rgba(10, 10, 10, 0.60)",
          70: "rgba(10, 10, 10, 0.70)",
          80: "rgba(10, 10, 10, 0.80)",
          90: "rgba(10, 10, 10, 0.90)",
        },
        white: {
          DEFAULT: "#FAFAFA",
          0: "rgba(250, 250, 250, 0.00)",
          4: "rgba(250, 250, 250, 0.04)",
          6: "rgba(250, 250, 250, 0.06)",
          8: "rgba(250, 250, 250, 0.08)",
          10: "rgba(250, 250, 250, 0.10)",
          12: "rgba(250, 250, 250, 0.12)",
          20: "rgba(250, 250, 250, 0.20)",
          24: "rgba(250, 250, 250, 0.24)",
          30: "rgba(250, 250, 250, 0.30)",
          40: "rgba(250, 250, 250, 0.40)",
          50: "rgba(250, 250, 250, 0.50)",
          60: "rgba(250, 250, 250, 0.60)",
          70: "rgba(250, 250, 250, 0.70)",
          80: "rgba(250, 250, 250, 0.80)",
          90: "rgba(250, 250, 250, 0.90)",
        },
        gray: {
          DEFAULT: "#AAAAAA",
          100: "#1A1A1A",
          200: "#2A2A2A",
          300: "#3A3A3A",
          400: "#4A4A4A",
          500: "#5A5A5A",
          600: "#6A6A6A",
          700: "#7A7A7A",
          800: "#8A8A8A",
          900: "#9A9A9A",
        },
        blue: "#15B9EB",
        orange: "#FF8C42",
        purple: "#8A2BE2",
        red: "#FF4D4D",
        yellow: "#F2E863",
      },
      spacing: {
        "4xs": "0.125rem", // 2px  (0.125 × 16px)
        "3xs": "0.25rem", // 4px  (0.25 × 16px)
        "2xs": "0.375rem", // 6px  (0.375 × 16px)
        xs: "0.5rem", // 8px  (0.5 × 16px)
        sm: "0.75rem", // 12px  (0.75 × 16px)
        md: "1rem", // 16px  (1 × 16px)
        lg: "1.25rem", // 20px  (1.25 × 16px)
        xl: "1.5rem", // 24px  (1.5 × 16px)
        "2xl": "2rem", // 32px  (2 × 16px)
        "3xl": "2.5rem", // 40px  (2.5 × 16px)
        "4xl": "3rem", // 48px  (3 × 16px)
        "5xl": "3.5rem", // 56px  (3.5 × 16px)
        "6xl": "4rem", // 64px  (4 × 16px)
        "7xl": "4.5rem", // 72px  (4.5 × 16px)
        "8xl": "5rem", // 80px  (5 × 16px)
        "9xl": "6rem", // 96px  (6 × 16px)
        "10xl": "7rem", // 112px  (7 × 16px)
        "11xl": "8rem", // 128px  (8 × 16px)
        "12xl": "9rem", // 144px  (9 × 16px)
        "13xl": "10rem", // 160px  (10 × 16px)
        "14xl": "11rem", // 176px  (11 × 16px)
        "15xl": "12rem", // 192px  (12 × 16px)
        "16xl": "13rem", // 208px  (13 × 16px)
        "17xl": "14rem", // 224px  (14 × 16px)
        "18xl": "15rem", // 240px  (15 × 16px)
        "19xl": "16rem", // 256px  (16 × 16px)
        "20xl": "18rem", // 288px  (18 × 16px)
        "21xl": "20rem", // 320px  (20 × 16px)
      },
      opacity: {
        4: "0.04",
      },
      screens: {
        xs: "460px",
      },
      borderColor: ["active"],
      keyframes: {
        "gradient-x": {
          "0%": { backgroundPosition: "100% 0%" },
          "100%": { backgroundPosition: "0% 0%" },
        },
      },
      animation: {
        "gradient-x": "gradient-x 2.8s linear infinite",
      },
    },
  },
  plugins: [],
};
export default config;
