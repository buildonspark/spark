#!/usr/bin/env node

const { generateMnemonic } = require("@scure/bip39");
const { wordlist } = require("@scure/bip39/wordlists/english");
const fs = require("fs");
const path = require("path");

/**
 * Generates a batch of BIP39 mnemonics
 * @param {number} count - Number of mnemonics to generate
 * @param {number} strength - Entropy strength in bits (128, 160, 192, 224, or 256)
 * @returns {string[]} Array of generated mnemonics
 */
function generateMnemonicBatch(count, strength = 256) {
  const mnemonics = [];

  for (let i = 0; i < count; i++) {
    const mnemonic = generateMnemonic(wordlist, strength);
    mnemonics.push(mnemonic);
  }

  return mnemonics;
}

/**
 * Saves mnemonics to a file
 * @param {string[]} mnemonics - Array of mnemonics to save
 * @param {string} filename - Output filename
 */
function saveMnemonicsToFile(mnemonics, filename) {
  const content = mnemonics.join("\n");
  const filepath = path.join(__dirname, "mnemonics", filename);

  // Ensure the mnemonics directory exists
  const dir = path.dirname(filepath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }

  fs.writeFileSync(filepath, content);
  console.log(`✅ Saved ${mnemonics.length} mnemonics to: ${filepath}`);
}

/**
 * Main function
 */
function main() {
  // Parse command line arguments
  const args = process.argv.slice(2);
  let count = 10; // Default number of mnemonics
  let strength = 256; // Default entropy strength (24 words)
  let outputFile = null;
  let displayToConsole = true;

  // Simple argument parsing
  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case "--count":
      case "-c":
        count = parseInt(args[++i], 10);
        if (isNaN(count) || count <= 0) {
          console.error("Error: Count must be a positive number");
          process.exit(1);
        }
        break;
      case "--strength":
      case "-s":
        strength = parseInt(args[++i], 10);
        const validStrengths = [128, 160, 192, 224, 256];
        if (!validStrengths.includes(strength)) {
          console.error(
            `Error: Strength must be one of: ${validStrengths.join(", ")}`,
          );
          process.exit(1);
        }
        break;
      case "--output":
      case "-o":
        outputFile = args[++i];
        break;
      case "--quiet":
      case "-q":
        displayToConsole = false;
        break;
      case "--help":
      case "-h":
        console.log(`
Usage: node generate-mnemonics.js [options]

Options:
  -c, --count <number>     Number of mnemonics to generate (default: 10)
  -s, --strength <bits>    Entropy strength in bits: 128, 160, 192, 224, 256 (default: 256)
                          128 bits = 12 words, 256 bits = 24 words
  -o, --output <filename>  Save mnemonics to file in mnemonics/ directory
  -q, --quiet             Don't display mnemonics on console (useful with --output)
  -h, --help              Show this help message

Examples:
  # Generate 10 mnemonics with 24 words each
  node generate-mnemonics.js

  # Generate 20 mnemonics with 12 words and save to file
  node generate-mnemonics.js -c 20 -s 128 -o test-wallets.txt

  # Generate 100 mnemonics quietly and save to file
  node generate-mnemonics.js -c 100 -o batch-wallets.txt -q
`);
        process.exit(0);
    }
  }

  console.log(
    `\n🔑 Generating ${count} mnemonic(s) with ${strength} bits of entropy (${(strength / 32) * 3} words)...\n`,
  );

  // Generate mnemonics
  const mnemonics = generateMnemonicBatch(count, strength);

  // Display to console if requested
  if (displayToConsole) {
    mnemonics.forEach((mnemonic, index) => {
      console.log(`${(index + 1).toString().padStart(3)}: ${mnemonic}`);
    });
    console.log();
  }

  // Save to file if requested
  if (outputFile) {
    saveMnemonicsToFile(mnemonics, outputFile);
  }

  if (!displayToConsole && !outputFile) {
    console.warn(
      "⚠️  Warning: Generated mnemonics but neither displayed nor saved them!",
    );
  }
}

// Run if called directly
if (require.main === module) {
  main();
}

// Export functions for use as a module
module.exports = {
  generateMnemonicBatch,
  saveMnemonicsToFile,
};
