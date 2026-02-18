export interface Template {
  dir: string;
  description: string;
  /** Steps shown after project creation. Use {pm} for package manager, {run} for run command. */
  steps: string[];
}

const WEB_STEPS = ["{pm} install", "{run} dev"];
const SERVER_STEPS = ["{pm} install", "{run} start:dev"];
const START_STEPS = ["{pm} install", "{run} start"];

export const TEMPLATES = {
  vite: {
    dir: "spark-vite-app",
    description: "React + Vite",
    steps: WEB_STEPS,
  },
  nextjs: {
    dir: "spark-nextjs-app",
    description: "Next.js",
    steps: WEB_STEPS,
  },
  "react-native": {
    dir: "spark-react-native-app",
    description: "React Native",
    steps: [
      "{pm} install",
      "# iOS",
      "cd ios && pod install",
      "{run} ios",
      "# Android",
      "{run} android",
    ],
  },
  expo: {
    dir: "spark-expo-react-native-app",
    description: "React Native (Expo)",
    steps: ["{pm} install", "{run} start"],
  },
  express: {
    dir: "spark-node-express",
    description: "Express.js server",
    steps: SERVER_STEPS,
  },
  nestjs: {
    dir: "nestjs-app",
    description: "NestJS server",
    steps: SERVER_STEPS,
  },
  webpack: {
    dir: "spark-webpack-react-app",
    description: "React + Webpack",
    steps: START_STEPS,
  },
  "browser-extension": {
    dir: "spark-browser-extension",
    description: "Browser extension",
    steps: ["{pm} install", "{run} build:chrome"],
  },
  cli: {
    dir: "spark-cli",
    description: "CLI application",
    steps: ["{pm} install", "{run} cli"],
  },
  bare: {
    dir: "spark-bare-app",
    description: "Bare runtime",
    steps: ["{pm} install", "{run} get-wallet-details"],
  },
  "nodejs-scripts": {
    dir: "nodejs-scripts",
    description: "Node.js scripts",
    steps: ["{pm} install", "npx tsx src/spark-sdk/get_balance.ts"],
  },
} as const satisfies Record<string, Template>;

export type TemplateName = keyof typeof TEMPLATES;

export const SPARK_PACKAGES = [
  "@buildonspark/spark-sdk",
  "@buildonspark/issuer-sdk",
  "@buildonspark/bare",
  "@buildonspark/spark-frost-bare-addon",
];
