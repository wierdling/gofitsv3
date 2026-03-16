$path = "$env:GOPATH\pkg\mod\fyne.io\fyne\v2@v2.4.5\container\scroll.go"; Get-Content $path | Select-String -Pattern "func \(s \*Scroll\)"
