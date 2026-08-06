$ErrorActionPreference = 'Stop'
$Utf8 = New-Object System.Text.UTF8Encoding($false, $true)

function Fail([string]$Message) {
    Write-Error $Message
    exit 1
}

function Pass([string]$Message) {
    Write-Host $Message
}

function Get-RepoFiles {
    return Get-ChildItem -Recurse -File -Force |
        Where-Object { $_.FullName -notlike '*\.git\*' } |
        ForEach-Object { Resolve-Path -Relative $_.FullName }
}

function Read-Utf8Text([string]$Path) {
    $resolved = Resolve-Path -LiteralPath $Path
    $bytes = [System.IO.File]::ReadAllBytes($resolved)
    return $script:Utf8.GetString($bytes)
}

function Assert-RequiredPath([string]$Path, [switch]$Directory) {
    if ($Directory) {
        if (-not (Test-Path -LiteralPath $Path -PathType Container)) { Fail "Missing directory: $Path" }
    } else {
        if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { Fail "Missing file: $Path" }
    }
}

function Assert-NoPatternMatches([string[]]$Files, [string[]]$Patterns, [string]$Label) {
    $hits = New-Object System.Collections.Generic.List[string]
    foreach ($file in $Files) {
        if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { continue }
        $text = Read-Utf8Text $file
        foreach ($pattern in $Patterns) {
            $regex = New-Object System.Text.RegularExpressions.Regex($pattern)
            $lines = $text -split "`n", 0, 'SimpleMatch'
            for ($i = 0; $i -lt $lines.Count; $i++) {
                $line = $lines[$i].TrimEnd("`r")
                if ($regex.IsMatch($line)) {
                    $lineNumber = $i + 1
                    $hits.Add("${file}:${lineNumber}:$line")
                }
            }
        }
    }
    if ($hits.Count -gt 0) {
        $hits | ForEach-Object { Write-Host $_ }
        Fail "$Label found matches."
    }
    Pass "$Label OK"
}

function Assert-LfOnly([string[]]$Files) {
    $hits = New-Object System.Collections.Generic.List[string]
    foreach ($file in $Files) {
        if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { continue }
        $bytes = [System.IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $file))
        if ($bytes -contains 13) { $hits.Add($file) }
    }
    if ($hits.Count -gt 0) {
        $hits | ForEach-Object { Write-Host $_ }
        Fail 'CRLF or bare CR line endings found.'
    }
    Pass 'LF line endings OK'
}

function Invoke-GitWhitespaceCheck {
    $git = Get-Command git -ErrorAction SilentlyContinue
    if (-not $git) { Fail 'git is required for whitespace verification.' }

    git diff --check
    if ($LASTEXITCODE -ne 0) { Fail 'git diff --check failed.' }

    git rev-parse --verify HEAD *> $null
    if ($LASTEXITCODE -eq 0) {
        git show --check --format= HEAD
        if ($LASTEXITCODE -ne 0) { Fail 'git show --check failed.' }
    }
    Pass 'Whitespace checks OK'
}

function Invoke-GoChecks {
    $goFiles = Get-ChildItem -Recurse -File -Filter '*.go' | Where-Object { $_.FullName -notlike '*\.git\*' }
    if (-not $goFiles) {
        Pass 'No Go files; Go checks skipped for scaffold phase'
        return
    }

    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) { Fail 'go is required when Go files exist.' }

    $unformatted = gofmt -l ($goFiles | ForEach-Object { $_.FullName })
    if ($LASTEXITCODE -ne 0) { Fail 'gofmt check failed.' }
    if ($unformatted) {
        $unformatted | ForEach-Object { Write-Host $_ }
        Fail 'Go files require gofmt.'
    }

    $packages = go list ./...
    if ($LASTEXITCODE -ne 0) { Fail 'go list ./... failed.' }
    if (-not $packages) {
        Pass 'No Go packages; go test and go vet skipped'
        return
    }

    go test ./...
    if ($LASTEXITCODE -ne 0) { Fail 'go test ./... failed.' }

    go vet ./...
    if ($LASTEXITCODE -ne 0) { Fail 'go vet ./... failed.' }
    Pass 'Go checks OK'
}

Assert-RequiredPath 'go.mod'
Assert-RequiredPath 'README.md'
Assert-RequiredPath 'README_CN.md'
Assert-RequiredPath 'LICENSE'
Assert-RequiredPath 'SECURITY.md'
Assert-RequiredPath 'THIRD_PARTY_NOTICES.md'
Assert-RequiredPath '.github/workflows/ci.yml'
Assert-RequiredPath '.github/workflows/release.yml'
Assert-RequiredPath 'docs/specs' -Directory
Assert-RequiredPath 'cmd/plugin' -Directory
Assert-RequiredPath 'internal/abi' -Directory
Pass 'Structure OK'

$files = @(Get-RepoFiles)
$pendingChinese = -join ([char[]](0x5F85, 0x786E, 0x8BA4))
$placeholderPatterns = @('TB' + 'D', 'TO' + 'DO', [System.Text.RegularExpressions.Regex]::Escape($pendingChinese))
Assert-NoPatternMatches -Files $files -Patterns $placeholderPatterns -Label 'Placeholder scan'

$privateKey = '-{5}BEGIN\s+(RSA\s+|OPENSSH\s+|EC\s+|DSA\s+)?PRIVATE\s+KEY-{5}'
$bearer = 'Bearer\s+[A-Za-z0-9._~+/=-]{20,}'
$personalMail = '[A-Za-z0-9._%+-]+@(gmail|qq|163|126|outlook|hotmail|icloud)\.com'
$ipv4 = '([0-9]{1,3}\.){3}[0-9]{1,3}'
$assignment = '(api[_-]?key|secret|credential|token|password)\s*[:=]\s*\S{8,}'
Assert-NoPatternMatches -Files $files -Patterns @($privateKey, $bearer, $personalMail, $ipv4, $assignment) -Label 'Public safety scan'

Assert-LfOnly -Files $files
Invoke-GitWhitespaceCheck
Invoke-GoChecks
Pass 'verify.ps1 completed'
