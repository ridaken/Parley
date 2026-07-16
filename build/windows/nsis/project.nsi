Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows you to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
## 
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the wails_tools.nsh file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "my-project" # Default "Parley"
## !define INFO_COMPANYNAME    "My Company" # Default "Vokac"
## !define INFO_PRODUCTNAME    "My Product Name" # Default "Parley"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "0.1.1"
## !define INFO_COPYRIGHT      "(c) Now, My Company" # Default "© 2026, My Company"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
## !define WAILS_INSTALL_SCOPE     "user"             # Default "machine" - set to "user" for per-user install ($LOCALAPPDATA) without UAC prompt
####
## Include the wails tools
####
!include "wails_tools.nsh"

# Wails' generated uninstaller macro calculates EstimatedSize by recursively
# scanning all of $INSTDIR. Older Parley releases stored the optional Nemotron
# model/runtime there, so an upgrade can spend minutes enumerating tens of
# thousands of persistent files after the application is already installed.
# Register the same uninstall metadata, but estimate only the shipped payload:
# the executable plus bundled CPU Whisper resources. Nemotron is provisioned
# and reused independently and must never make an application upgrade stall.
!macro parley.writeUninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    ${GetSize} "$INSTDIR\resources\whisper" "/S=0K" $0 $1 $2
    FileOpen $1 "$INSTDIR\${PRODUCT_EXECUTABLE}" r
    FileSeek $1 0 END $2
    FileClose $1
    IntOp $2 $2 + 1023
    IntOp $2 $2 / 1024
    IntOp $0 $0 + $2

    SetRegView 64
    !if "${WAILS_INSTALL_SCOPE}" == "user"
        WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
        WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
        WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
        WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE}"
        WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
        WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
        WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" "$0"
    !else
        WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
        WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
        WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
        WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE}"
        WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
        WriteRegStr HKLM "${UNINST_KEY}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
        WriteRegDWORD HKLM "${UNINST_KEY}" "EstimatedSize" "$0"
    !endif
!macroend

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uninstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
!if "${WAILS_INSTALL_SCOPE}" == "user"
    InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
!else
    InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!endif
ShowInstDetails show # This will always show the installation details.

Var IsUpgrade
Var NemotronRoot
Var NemotronProvisionRequested

Function .onInit
   !insertmacro wails.checkArchitecture

   StrCpy $IsUpgrade "0"
   StrCpy $NemotronProvisionRequested "0"

   # On update, honor the location of a previously installed copy (recorded below
   # as InstallLocation) so we overwrite it in place instead of installing a
   # second copy when the user picked a non-default directory.
   SetRegView 64
   !if "${WAILS_INSTALL_SCOPE}" == "user"
       ReadRegStr $0 HKCU "${UNINST_KEY}" "InstallLocation"
   !else
       ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
   !endif
   ${If} $0 != ""
       StrCpy $INSTDIR $0
       StrCpy $IsUpgrade "1"
   ${EndIf}
FunctionEnd

Section
    !insertmacro wails.setShellContext

    # When updating, close any running instance so its locked files (Parley.exe,
    # whisper DLLs) can be overwritten. Harmless no-op on a fresh install.
    nsExec::Exec 'cmd /C taskkill /T /F /IM "${PRODUCT_EXECUTABLE}" >NUL 2>&1'
    Pop $0

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR

    !insertmacro wails.files

    # Bundle only CPU Whisper plus the small Nemotron provisioner. Generated
    # Nemotron model/runtime/cache directories in a developer checkout can total
    # more than 10 GiB and must never be swept into a locally-built installer.
    # Replace shipped Whisper artifacts on upgrade, but preserve the legacy
    # resources/nemotron directory for users provisioned by older releases.
    RMDir /r "$INSTDIR\resources\whisper"
    SetOutPath "$INSTDIR\resources\whisper"
    File /r "..\..\..\resources\whisper\*"
    SetOutPath "$INSTDIR\resources\nemotron"
    File "..\..\..\resources\nemotron\download_model.py"
    File "..\..\..\resources\nemotron\provision.ps1"
    File "..\..\..\resources\nemotron\server.py"
    File "..\..\..\resources\nemotron\setup.ps1"
    File "..\..\..\resources\nemotron\validate_install.py"

    # New releases keep the multi-GB installation in per-user LocalAppData so
    # installed and development builds can share it. Continue recognizing the
    # legacy beside-the-exe location. A .source-root marker may point at a valid
    # older checkout installation without copying or downloading it again.
    StrCpy $NemotronRoot "$LOCALAPPDATA\Parley\nemotron"
    IfFileExists "$NemotronRoot\.ready" nemotron_present nemotron_check_shared_source

    nemotron_check_shared_source:
        IfFileExists "$NemotronRoot\.source-root" nemotron_present nemotron_check_legacy

    nemotron_check_legacy:
    IfFileExists "$INSTDIR\resources\nemotron\.ready" nemotron_present nemotron_probe_gpu

    nemotron_probe_gpu:
        # NSIS is a 32-bit process even for an amd64 installer. Without disabling
        # filesystem redirection, System32 resolves to SysWOW64, where NVIDIA's
        # 64-bit nvidia-smi.exe is not installed.
        ${DisableX64FSRedirection}
        nsExec::ExecToStack '"$SYSDIR\nvidia-smi.exe" -L'
        ${EnableX64FSRedirection}
        Pop $1
        Pop $2
        StrCmp $1 "0" nemotron_gpu_found nemotron_no_gpu

    nemotron_gpu_found:
        StrCmp $IsUpgrade "0" nemotron_provision
        IfSilent nemotron_silent_skip
        MessageBox MB_YESNO|MB_ICONQUESTION "Parley found an NVIDIA GPU, but Nemotron 3.5 ASR Streaming is not installed.$\r$\n$\r$\nFind or download it now? After Parley finishes installing, a separate progress window will open. You can close that window at any time to cancel; partial downloads are retained so a later attempt can resume. CPU Whisper remains available if you choose No." /SD IDNO IDYES nemotron_provision IDNO nemotron_declined

    nemotron_provision:
        StrCpy $NemotronProvisionRequested "1"
        DetailPrint "Nemotron setup queued in a separate cancellable progress window."
        Goto nemotron_done

    nemotron_present:
        DetailPrint "Existing Nemotron installation detected; Parley will reuse it automatically."
        Goto nemotron_done

    nemotron_no_gpu:
        DetailPrint "No NVIDIA GPU detected; using bundled CPU Whisper."
        Goto nemotron_done

    nemotron_declined:
        DetailPrint "Nemotron download declined; using bundled CPU Whisper."
        Goto nemotron_done

    nemotron_silent_skip:
        DetailPrint "Silent upgrade detected; skipping optional Nemotron download and using bundled CPU Whisper."
        Goto nemotron_done

    nemotron_done:

    SetOutPath $INSTDIR

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    !insertmacro parley.writeUninstaller

    # Record where we installed so a future update can find and overwrite this copy.
    SetRegView 64
    !if "${WAILS_INSTALL_SCOPE}" == "user"
        WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
    !else
        WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
    !endif

    # Never hold the installer UI hostage to Python/PyTorch/Hugging Face work.
    # The visible asynchronous window carries progress and may be closed to
    # cancel. setup.ps1 retains caches/partial files so rerunning can resume.
    StrCmp $NemotronProvisionRequested "1" 0 nemotron_launch_done
    DetailPrint "Opening cancellable Nemotron setup window..."
    ${DisableX64FSRedirection}
    ClearErrors
    Exec '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\resources\nemotron\provision.ps1" -InstallRoot "$NemotronRoot"'
    ${EnableX64FSRedirection}
    IfErrors 0 nemotron_launch_done
    DetailPrint "Could not launch Nemotron setup; Parley will use bundled CPU Whisper."

    nemotron_launch_done:
SectionEnd

Section "uninstall" 
    !insertmacro wails.setShellContext

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd
