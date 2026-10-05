# Gera dist\AnglerOtimizador.exe (ícone, versão e manifesto embutidos).
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not (Get-Command go-winres -ErrorAction SilentlyContinue)) {
    go install github.com/tc-hib/go-winres@latest
}
go-winres make --arch amd64
go vet ./...
New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -ldflags "-H windowsgui" -o dist\AnglerOtimizador.exe .
Get-Item dist\AnglerOtimizador.exe | Select-Object Name, Length
