/** @type {import('tailwindcss').Config} */
const config = {
  content: ["./src/**/*.{js,ts,jsx,tsx}"],
  theme: {
    extend: {
      fontFamily: {
        inter: ["var(--font-inter)", "sans-serif"],
        sans: ["var(--font-inter)", "sans-serif"],
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
