#!/usr/bin/env node
/**
 * Simple SVG Theme Converter
 * Based on Jonathan Harrell's approach: https://www.jonathanharrell.com/blog/light-dark-mode-svgs
 * 
 * This script replaces hex colors in SVG files with CSS variables for automatic light/dark mode support.
 */

const fs = require('fs');
const path = require('path');

// Color mapping: hex codes to CSS variables
const hexToCssVariables = {
  // Background colors (case-insensitive)
  '#ffffff': 'var(--svg-bg)',
  '#fff': 'var(--svg-bg)',
  'white': 'var(--svg-bg)',
  '#f5f5f5': 'var(--svg-secondary-bg)',

  // Text colors
  '#000000': 'var(--svg-text)',
  '#000': 'var(--svg-text)',
  'black': 'var(--svg-text)',
  '#333333': 'var(--svg-secondary-text)',
  '#666666': 'var(--svg-secondary-text)',

  // Accent colors
  '#2875e2': 'var(--svg-accent-blue)',
  '#a166ff': 'var(--svg-accent-purple)',
  '#28a745': 'var(--svg-accent-green)',
  '#dc3545': 'var(--svg-accent-red)',
};

// CSS template for the SVG
const cssTemplate = `<style><![CDATA[
:root {
  --svg-bg: #ffffff;
  --svg-text: #000000;
  --svg-secondary-bg: #f5f5f5;
  --svg-secondary-text: #333333;
  --svg-accent-blue: #2875e2;
  --svg-accent-purple: #a166ff;
  --svg-accent-green: #28a745;
  --svg-accent-red: #dc3545;
}

@media (prefers-color-scheme: dark) {
  :root {
    --svg-bg: #121212;
    --svg-text: #ffffff;
    --svg-secondary-bg: #1a1a1a;
    --svg-secondary-text: #c1c1c1;
    --svg-accent-blue: #5597f5;
    --svg-accent-purple: #9f6df0;
    --svg-accent-green: #4caf50;
    --svg-accent-red: #ff6b7a;
  }
}

* {
  transition: fill 0.3s ease, stroke 0.3s ease;
}

text {
  font-family: "Helvetica", "Arial", sans-serif;
}
]]></style>`;

function processFile(inputFile, outputFile) {
  console.log(`Processing: ${inputFile}`);
  
  fs.readFile(inputFile, 'utf-8', (err, data) => {
    if (err) {
      console.error(`Error reading file: ${err}`);
      return;
    }

    let svgContent = data;

    // Replace hex codes with CSS variables
    Object.keys(hexToCssVariables).forEach((hex) => {
      const cssVar = hexToCssVariables[hex];

      // Create multiple regex patterns for different formats
      const patterns = [
        new RegExp(`fill="${hex}"`, 'gi'),
        new RegExp(`stroke="${hex}"`, 'gi'),
        new RegExp(`fill='${hex}'`, 'gi'),
        new RegExp(`stroke='${hex}'`, 'gi'),
        new RegExp(`fill:\\s*${hex.replace('#', '#?')}`, 'gi'),
        new RegExp(`stroke:\\s*${hex.replace('#', '#?')}`, 'gi'),
      ];

      patterns.forEach(pattern => {
        svgContent = svgContent.replace(pattern, (match) => {
          return match.replace(hex, cssVar);
        });
      });
    });

    // Remove existing style tags to avoid conflicts
    svgContent = svgContent.replace(/<style[^>]*>[\s\S]*?<\/style>/gi, '');
    
    // Remove light-dark() functions
    svgContent = svgContent.replace(/light-dark\([^)]+\)/gi, 'var(--svg-text)');
    
    // Insert CSS after the opening SVG tag
    const svgTagMatch = svgContent.match(/<svg[^>]*>/);
    if (svgTagMatch) {
      const insertIndex = svgContent.indexOf(svgTagMatch[0]) + svgTagMatch[0].length;
      svgContent = svgContent.slice(0, insertIndex) + cssTemplate + svgContent.slice(insertIndex);
    }

    // Clean up the SVG tag
    svgContent = svgContent.replace(/<svg[^>]*>/, (match) => {
      // Remove problematic attributes
      let cleanTag = match
        .replace(/style="[^"]*"/gi, '')
        .replace(/content="[^"]*"/gi, '')
        .replace(/xmlns:ns\d+="[^"]*"/gi, '')
        .replace(/\s+/g, ' ')
        .trim();
      
      // Ensure proper namespace
      if (!cleanTag.includes('xmlns=')) {
        cleanTag = cleanTag.replace('<svg', '<svg xmlns="http://www.w3.org/2000/svg"');
      }
      if (!cleanTag.includes('xmlns:xlink=')) {
        cleanTag = cleanTag.replace('<svg', '<svg xmlns:xlink="http://www.w3.org/1999/xlink"');
      }
      
      return cleanTag;
    });

    // Write the processed file
    fs.writeFile(outputFile, svgContent, 'utf-8', (err) => {
      if (err) {
        console.error(`Error writing file: ${err}`);
        return;
      }
      console.log(`✅ Created: ${outputFile}`);
    });
  });
}

function main() {
  const args = process.argv.slice(2);
  
  if (args.length === 0) {
    console.log('Usage: node simple-svg-themer.js <input.svg> [output.svg]');
    console.log('Example: node simple-svg-themer.js diagram.svg diagram-themed.svg');
    process.exit(1);
  }

  const inputFile = args[0];
  const outputFile = args[1] || inputFile.replace('.svg', '-themed.svg');

  if (!fs.existsSync(inputFile)) {
    console.error(`Error: Input file '${inputFile}' not found`);
    process.exit(1);
  }

  if (!inputFile.toLowerCase().endsWith('.svg')) {
    console.error('Error: Input file must be an SVG file');
    process.exit(1);
  }

  processFile(inputFile, outputFile);
}

if (require.main === module) {
  main();
}

module.exports = { processFile, hexToCssVariables };
