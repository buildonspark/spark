/** @type {import('tailwindcss').Config} */
const config = {
  content: ["./src/**/*.{js,ts,jsx,tsx}"],
  theme: {
    extend: {
      fontFamily: {
        decimal: ["var(--font-decimal)", "sans-serif"],
        "geist-sans": ["var(--font-geist-sans)", "sans-serif"],
        "geist-mono": ["var(--font-geist-mono)", "monospace"],
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
