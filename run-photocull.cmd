@echo off
setlocal

rem ---------------------------------------------------------------------------
rem  photocull launcher for Windows.
rem
rem  Double-click this file to open the app. It builds the binary first if the
rem  repository has not been built yet, so a fresh clone works with one click.
rem
rem  Why this exists instead of "just double-click photocull.exe": a security
rem  suite scores the launch context, not only the file. The chain
rem  explorer.exe -> unsigned .exe -> binds a local port is the signature of the
rem  commonest malware delivery path, and at least one suite (McAfee, measured on
rem  a real machine) kills photocull about five seconds in, before it opens its
rem  port or draws a window. The binary is a windowsgui build, so there is no
rem  console and no error message: the symptom is that nothing happens at all.
rem  Launching from here puts cmd.exe in the chain instead, which is scored
rem  differently and starts normally.
rem
rem  This is a workaround, not a fix. The fixes are an antivirus exclusion for
rem  this folder, or an Authenticode-signed binary.
rem ---------------------------------------------------------------------------

set "EXE=%~dp0photocull.exe"
if exist "%EXE%" goto launch

set "EXE=%~dp0dist\photocull.exe"
if exist "%EXE%" goto launch

echo No photocull.exe here yet - building it from source.
echo.

rem Find the Go toolchain: PATH first, then the usual install locations, so a
rem portable SDK that was never added to PATH still works.
set "GOEXE="
for %%G in (go.exe) do if exist "%%~$PATH:G" set "GOEXE=%%~$PATH:G"
if not defined GOEXE if exist "%ProgramFiles%\Go\bin\go.exe" set "GOEXE=%ProgramFiles%\Go\bin\go.exe"
if not defined GOEXE if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" set "GOEXE=%LOCALAPPDATA%\Programs\Go\bin\go.exe"
if not defined GOEXE if exist "%USERPROFILE%\go-sdk\go\bin\go.exe" set "GOEXE=%USERPROFILE%\go-sdk\go\bin\go.exe"

if not defined GOEXE (
    echo Go was not found on PATH or in the usual install locations.
    echo.
    echo   Install Go 1.26 or newer from https://go.dev/dl/ and run this file
    echo   again, or put a prebuilt photocull.exe next to this script.
    echo.
    pause
    exit /b 1
)

rem -H=windowsgui suppresses the console window the app would otherwise flash on
rem every launch. It also means the scan/clean/serve subcommands print nowhere,
rem so build a plain console binary with "go build ./cmd/photocull" for those.
pushd "%~dp0"
"%GOEXE%" build -trimpath -ldflags "-s -w -H=windowsgui" -o "dist\photocull.exe" ".\cmd\photocull"
set "BUILDRC=%ERRORLEVEL%"
popd

if not "%BUILDRC%"=="0" (
    echo.
    echo The build failed ^(exit code %BUILDRC%^).
    echo.
    pause
    exit /b %BUILDRC%
)

set "EXE=%~dp0dist\photocull.exe"

:launch
rem "start" hands the app off and returns, so this console window closes itself
rem instead of hanging around for as long as photocull is open.
start "" "%EXE%" %*
exit /b 0
