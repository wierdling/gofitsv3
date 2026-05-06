$ErrorActionPreference = "Stop"

Remove-Item -Recurse -Force .\dist -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force .\dist | Out-Null

go mod tidy
go test ./...

Push-Location .\cmd
fyne package -os windows -icon ..\assets\Icon.png -release
Pop-Location

Move-Item .\cmd\GoFitsV3.exe .\dist\GoFitsV3.exe -Force