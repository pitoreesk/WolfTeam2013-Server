@echo off
setlocal
set "CSC=%SystemRoot%\Microsoft.NET\Framework64\v4.0.30319\csc.exe"
if not exist "%CSC%" set "CSC=%SystemRoot%\Microsoft.NET\Framework\v4.0.30319\csc.exe"
if not exist "%CSC%" (
    echo [HATA] .NET Framework 4.x csc.exe bulunamadi.
    exit /b 1
)

echo WolfTeamLauncher.exe derleniyor...
"%CSC%" /nologo /target:winexe /optimize+ /win32manifest:app.manifest ^
    /reference:System.dll /reference:System.Drawing.dll /reference:System.Windows.Forms.dll ^
    /out:WolfTeamLauncher.exe WolfTeamLauncher.cs

if errorlevel 1 (
    echo [HATA] Derleme basarisiz.
    exit /b 1
)
echo [OK] WolfTeamLauncher.exe hazir.
endlocal
