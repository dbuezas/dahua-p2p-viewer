# Builds Camaras.exe on Windows with the C# compiler that comes with .NET Framework 4 (no Visual Studio needed).
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
& "$env:WINDIR\Microsoft.NET\Framework64\v4.0.30319\csc.exe" /nologo /platform:x64 /target:winexe `
  /out:"$here\Camaras.exe" /r:System.Security.dll /r:System.Windows.Forms.dll /r:System.Drawing.dll "$here\Camaras.cs"
