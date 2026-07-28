$ErrorActionPreference = "Stop"

Remove-Item -Recurse -Force .\dist -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force .\dist | Out-Null

go mod tidy
go test ./badpix ./cmd ./internal/... ./webpwriter

Push-Location .\cmd
fyne package -os windows -icon ..\assets\icon.png -release
Pop-Location

Move-Item .\cmd\GoFitsV3.exe .\dist\GoFitsV3.exe -Force
