# Building NimbusBackup to MSI on Windows

## Quick Start (tl;dr)

On a Windows machine with Go 1.25+, Node 20+, and admin privileges:

```powershell
# 1. Install WiX Toolset 3.14+
choco install wixtoolset -y

# 2. Build the GUI (produces NimbusBackup.exe + NimbusBackupSVC.exe)
cd gui
wails build -clean -platform windows/amd64

# 3. Build the MSI (uses the binaries from step 2)
cd ..\installer\wix
build.bat

# Result: NimbusBackup.msi in installer/wix/
```

---

## Full Step-by-Step

### Prerequisites

- **Windows 10/11** (64-bit)
- **Go 1.25+** — [download](https://golang.org/dl/)
- **Node.js 20+** — [download](https://nodejs.org/)
- **WiX Toolset 3.14+** — [download](https://wixtoolset.org/) or:
  ```powershell
  choco install wixtoolset -y
  ```
- **Admin privileges** on the machine
- The latest `gui/frontend` npm dependencies installed

### Step 1: Clone and Prepare

```powershell
git clone https://github.com/CJInvent/NimbusBackupClient.git nimbus
cd nimbus
```

### Step 2: Verify Go & Node

```powershell
go version          # Should be 1.25.x or later
node --version      # Should be v20.x or later
npm --version       # Should be 10.x or later
```

### Step 3: Install Wails

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

Verify:
```powershell
wails --version    # Should be 2.8.0 or later
```

### Step 4: Build the GUI Binary

```powershell
cd gui
wails build -clean -platform windows/amd64
```

This produces two executables in `gui/build/bin/`:
- **NimbusBackup.exe** — The GUI application (user-facing)
- **NimbusBackupSVC.exe** — The Windows Service (runs scheduled backups with admin privs)

### Step 5: Verify Binaries Exist

```powershell
ls gui\build\bin\
# Expected output:
#   NimbusBackup.exe
#   NimbusBackupSVC.exe
```

If either is missing, the MSI build will fail. Check `gui/wails.json` to verify build output locations.

### Step 6: Install WiX Toolset (if not already done)

```powershell
# Via Chocolatey (easiest)
choco install wixtoolset -y

# Or manually download from https://wixtoolset.org/
```

Verify WiX is in PATH:
```powershell
candle.exe --version    # Should print version info
light.exe --version     # Should print version info
```

If not found, add to PATH:
```powershell
$env:PATH += ";C:\Program Files (x86)\WiX Toolset v3.14\bin"
```

### Step 7: Build the MSI

```powershell
cd installer\wix
build.bat
```

The script will:
1. Clean previous build artifacts (`.wixobj`, `.wixpdb`, `.msi`)
2. Compile `Product.wxs` → `Product.wixobj` (via `candle.exe`)
3. Link and create `NimbusBackup.msi` (via `light.exe`)

**Success output:**
```
SUCCESS! MSI created: NimbusBackup.msi

Press any key to continue . . .
```

The MSI is now at: **`installer/wix/NimbusBackup.msi`**

### Step 8: Test the MSI

1. **Double-click** `NimbusBackup.msi` to launch the installer
2. Follow the wizard (standard Windows MSI flow)
3. After install, verify the service exists:
   ```powershell
   Get-Service NimbusBackup
   # Output: NimbusBackup     Running    Auto       (or Stopped if not yet started)
   ```
4. The GUI shortcut appears in Start Menu → Nimbus Backup

---

## Version Numbering

The MSI version is read from `gui/wails.json` > `"productVersion"`:

```json
{
  "productVersion": "0.2.112",
  ...
}
```

**For CI/Release builds**, override via candle preprocessor:

```powershell
candle.exe "-dProductVersion=0.2.112" Product.wxs -ext WixUIExtension -ext WixUtilExtension
```

---

## What the MSI Does

- Installs NimbusBackup to `C:\Program Files\NimbusBackup\`
- Registers a Windows Service (`NimbusBackupSVC.exe`)
- Configures service to run as LocalSystem (guarantees admin/VSS access)
- Creates Start Menu shortcut
- On uninstall, optionally preserves config in `C:\ProgramData\NimbusBackup\`

---

## Troubleshooting

### Error: "WiX Toolset not found in PATH"

```powershell
choco install wixtoolset -y
# OR manually add to PATH:
$env:PATH += ";C:\Program Files (x86)\WiX Toolset v3.14\bin"
```

### Error: "NimbusBackup.exe not found"

The WiX `Product.wxs` expects binaries at:
```
../../gui/build/bin/NimbusBackup.exe
../../gui/build/bin/NimbusBackupSVC.exe
```

Ensure you ran `wails build` successfully (step 4).

### Error: "Compilation failed" or "Linking failed"

Check for typos in `Product.wxs`. Common issues:
- File paths with wrong case (Windows file paths are case-insensitive, but WiX is strict)
- Missing extensions in `candle`/`light` commands (always use `-ext WixUIExtension -ext WixUtilExtension`)

### Service doesn't start after install

```powershell
# Check service status
Get-Service NimbusBackup

# Try to start manually
Start-Service NimbusBackup

# Check Event Viewer for errors
Get-EventLog -LogName Application -Source NimbusBackupSVC -Newest 5
```

Common cause: Binary at install location is corrupted or the GUI build failed.

---

## Distribution

Once tested, the MSI can be distributed via:
- **GitHub Releases** (automated via CI)
- **Website** (direct download)
- **Group Policy** (GPO deployment in enterprises)
- **Intune** (MDM for O365 / cloud-managed machines)

---

## What's Included in Machine Backup (0.2.112+)

If you've enabled the full-volume backup feature (as of this build), the MSI includes:
- **NimbusBackup.exe** — GUI now has "Machine (full disk)" option
- **ListPhysicalDisks()** binding — detects disks for the UI
- **RunMachineBackup()** — full-disk FIDX imaging backend

No additional files are needed; it's all compiled into the service binary.

---

## GitHub Actions Build

The repo's CI (`.github/workflows/build-and-release.yml`) automates this on every tag:

```yaml
- name: Install WiX
  run: choco install wixtoolset -y

- name: Build MSI
  working-directory: installer/wix
  run: |
    candle.exe "-dProductVersion=${{ steps.version.outputs.VERSION_MSI }}" Product.wxs -ext WixUIExtension -ext WixUtilExtension
    light.exe Product.wixobj -ext WixUIExtension -ext WixUtilExtension -out NimbusBackup.msi
```

Tag a release, and the MSI is automatically built and uploaded to GitHub Releases.

---

## Next Steps

1. **Test the MSI** on a Windows machine (VM is fine)
2. **Verify the service starts** at boot
3. **Test scheduled backups** run reliably
4. **Sign the MSI** (code signing cert) to eliminate Defender warnings
5. **Release to GitHub Releases**
