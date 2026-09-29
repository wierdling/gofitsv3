[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)]
    [ValidateScript({ Test-Path -LiteralPath $_ -PathType Container })]
    [string]$SourceFolder,

    [switch]$Move
)

$sourcePath = (Resolve-Path -LiteralPath $SourceFolder).Path
$organizedPath = Join-Path -Path $sourcePath -ChildPath 'Organized'
$scriptPath = [IO.Path]::GetFullPath($PSCommandPath)

# Use a trailing separator so a folder such as "Organized Files" is not excluded.
$organizedPrefix = $organizedPath.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar

$files = Get-ChildItem -LiteralPath $sourcePath -File -Recurse | Where-Object {
    -not $_.FullName.StartsWith($organizedPrefix, [StringComparison]::OrdinalIgnoreCase) -and
    -not [String]::Equals($_.FullName, $scriptPath, [StringComparison]::OrdinalIgnoreCase)
}

if (-not $files) {
    Write-Host 'No files found outside the Organized folder.'
    return
}

$types = $files |
    ForEach-Object { if ($_.Extension) { $_.Extension.TrimStart('.').ToLowerInvariant() } else { 'NoExtension' } } |
    Sort-Object -Unique

Write-Host 'File types found:'
$types | ForEach-Object { Write-Host "  $_" }

if (-not (Test-Path -LiteralPath $organizedPath)) {
    New-Item -ItemType Directory -Path $organizedPath | Out-Null
}

foreach ($file in $files) {
    $typeName = if ($file.Extension) { $file.Extension.TrimStart('.').ToLowerInvariant() } else { 'NoExtension' }
    $typeFolder = Join-Path -Path $organizedPath -ChildPath $typeName

    if (-not (Test-Path -LiteralPath $typeFolder)) {
        New-Item -ItemType Directory -Path $typeFolder | Out-Null
    }

    $destination = Join-Path -Path $typeFolder -ChildPath $file.Name
    $suffix = 1
    while (Test-Path -LiteralPath $destination) {
        $destination = Join-Path -Path $typeFolder -ChildPath ('{0} ({1}){2}' -f $file.BaseName, $suffix, $file.Extension)
        $suffix++
    }

    $action = if ($Move) { 'Move' } else { 'Copy' }
    if ($PSCmdlet.ShouldProcess($file.FullName, "$action to $destination")) {
        if ($Move) {
            Move-Item -LiteralPath $file.FullName -Destination $destination
        } else {
            Copy-Item -LiteralPath $file.FullName -Destination $destination
        }
    }
}

Write-Host "Completed. Files were $(if ($Move) { 'moved' } else { 'copied' }) to: $organizedPath"
