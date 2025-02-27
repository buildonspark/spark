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
    },
  },
  plugins: [],
};
export default config;
