# create-spark-app

Scaffold a new [Spark](https://github.com/buildonspark/spark) application from a template.

## Quick Start

```bash
npx @buildonspark/create-spark-app my-app --template vite
cd my-app
npm install
npm run dev
```

Or run without arguments for an interactive template picker:

```bash
npx @buildonspark/create-spark-app my-app
```

## Templates

| Template            | Description         |
| ------------------- | ------------------- |
| `vite`              | React + Vite        |
| `nextjs`            | Next.js             |
| `react-native`      | React Native        |
| `expo`              | React Native (Expo) |
| `express`           | Express.js server   |
| `nestjs`            | NestJS server       |
| `webpack`           | React + Webpack     |
| `browser-extension` | Browser extension   |
| `cli`               | CLI application     |
| `bare`              | Bare runtime        |
| `nodejs-scripts`    | Node.js scripts     |

## Options

```
Usage: create-spark-app [project-name] [options]

Options:
  --template, -t  Template to use
  --branch, -b    Git branch to fetch templates from (default: main)
  --help, -h      Show help
```

## How It Works

Templates are fetched directly from the [buildonspark/spark](https://github.com/buildonspark/spark) repository on GitHub. The generated project is a standalone copy with:

- Your project name in `package.json`
- `@buildonspark/spark-sdk` set to `latest`
- No workspace dependencies or private flags
