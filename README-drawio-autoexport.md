# VS Code Draw.io Auto-Export Setup

## ✅ Configuration Complete

The VS Code Draw.io auto-export has been configured with the following settings in `.vscode/settings.json`:

```json
{
  "hediet.vscode-drawio.export": {
    "docs/diagrams/arch.drawio.svg": [
      {
        "format": "svg",
        "outputPath": "docs/diagrams/arch.svg",
        "embedSvg": false,
        "embedDiagram": false,
        "crop": true,
        "transparentBackground": true
      }
    ]
  },
  "hediet.vscode-drawio.local-storage": "eyJpc0VuYWJsZWQiOnRydWV9",
  "hediet.vscode-drawio.codeLinkActivated": true,
  "files.associations": {
    "*.drawio.svg": "drawio"
  }
}
```

## 🚀 How to Use Auto-Export

1. **Open VS Code in the project directory:**
   ```bash
   code .
   ```

2. **Open the Draw.io file:**
   - Open `docs/diagrams/arch.drawio.svg` in VS Code
   - The file should open in the Draw.io editor interface

3. **Edit and save:**
   - Make changes to your diagram
   - Save the file (Ctrl+S or Cmd+S)
   - `docs/diagrams/arch.svg` will automatically update

## 🔧 Troubleshooting

### Auto-export not working?

**Requirements:**
- VS Code must be running
- The `arch.drawio.svg` file must be open in VS Code
- The hediet.vscode-drawio extension must be installed

**Quick fixes:**
1. Reload VS Code window: Ctrl+Shift+P → "Developer: Reload Window"
2. Close and reopen the Draw.io file
3. Check that the extension is installed: `code --list-extensions | grep drawio`

### Manual Export (Fallback)

If auto-export fails, you can manually export:

1. Open `docs/diagrams/arch.drawio.svg` in VS Code
2. Press Ctrl+Shift+P (Cmd+Shift+P on Mac)
3. Type "Draw.io: Export"
4. Select "Export as SVG"
5. Save as: `docs/diagrams/arch.svg`
6. Ensure these options are selected:
   - ✅ Crop
   - ✅ Transparent Background
   - ❌ Embed SVG
   - ❌ Embed Diagram

## 📁 File Structure

```
docs/diagrams/
├── arch.drawio.svg    # Source file (edit this)
└── arch.svg          # Auto-exported clean SVG (for GitHub display)
```

## 🎯 Benefits

- **Clean exports:** No Draw.io metadata in the SVG
- **Automatic cropping:** Removes excess whitespace
- **Transparent background:** Better integration with GitHub
- **Version control friendly:** Both source and export are tracked
- **Seamless workflow:** Edit → Save → Auto-export

## 📝 Workflow

1. Edit `docs/diagrams/arch.drawio.svg` in VS Code Draw.io editor
2. Save the file (auto-export triggers)
3. Commit both files to Git
4. GitHub displays the clean `arch.svg` in documentation

The auto-export ensures your GitHub documentation always shows the latest diagram version without manual export steps.
