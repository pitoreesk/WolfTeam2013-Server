[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Test-IsSafePublicationAddress {
    param([Parameter(Mandatory)][Net.IPAddress]$Address)

    if ([Net.IPAddress]::IsLoopback($Address)) {
        return $true
    }

    $bytes = $Address.GetAddressBytes()
    if ($Address.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork) {
        if ($bytes[0] -eq 0 -or
            $bytes[0] -eq 10 -or
            ($bytes[0] -eq 100 -and $bytes[1] -ge 64 -and $bytes[1] -le 127) -or
            ($bytes[0] -eq 169 -and $bytes[1] -eq 254) -or
            ($bytes[0] -eq 172 -and $bytes[1] -ge 16 -and $bytes[1] -le 31) -or
            ($bytes[0] -eq 192 -and $bytes[1] -eq 168) -or
            ($bytes[0] -eq 192 -and $bytes[1] -eq 0 -and $bytes[2] -in 0, 2) -or
            ($bytes[0] -eq 198 -and $bytes[1] -in 18, 19) -or
            ($bytes[0] -eq 198 -and $bytes[1] -eq 51 -and $bytes[2] -eq 100) -or
            ($bytes[0] -eq 203 -and $bytes[1] -eq 0 -and $bytes[2] -eq 113) -or
            $bytes[0] -ge 224) {
            return $true
        }
        return $false
    }

    if ($Address.Equals([Net.IPAddress]::IPv6Any) -or
        $bytes[0] -eq 0xff -or
        ($bytes[0] -band 0xfe) -eq 0xfc -or
        ($bytes[0] -eq 0xfe -and ($bytes[1] -band 0xc0) -eq 0x80) -or
        ($bytes[0] -eq 0x20 -and $bytes[1] -eq 0x01 -and
            $bytes[2] -eq 0x0d -and $bytes[3] -eq 0xb8)) {
        return $true
    }
    return $false
}

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
Push-Location -LiteralPath $repositoryRoot
try {
    $publicationFiles = @(
        & git ls-files --cached --others --exclude-standard -- . |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf }
    )
    if ($LASTEXITCODE -ne 0) {
        throw 'git ls-files failed'
    }

    $blockedExtensions = @(
        '.7z', '.bin', '.dat', '.dll', '.dylib', '.exe', '.jar', '.pdb',
        '.pyc', '.rar', '.so', '.xfs', '.zip'
    )
    $blockedFiles = @(
        $publicationFiles | Where-Object {
            [IO.Path]::GetExtension($_).ToLowerInvariant() -in $blockedExtensions
        }
    )

    $clientPayloads = @(
        $publicationFiles | Where-Object {
            $_ -match '(^|/)patches/client/'
        }
    )

    $requiredLicenseFiles = @(
        'LICENSE.md',
        'go-gameserver/LICENSES/README.md',
        'go-gameserver/LICENSES/project-MIT.txt',
        'go-gameserver/LICENSES/predecessor-MIT.txt',
        'go-gameserver/LICENSES/pgx-MIT.txt',
        'go-gameserver/LICENSES/pgpassfile-MIT.txt',
        'go-gameserver/LICENSES/pgservicefile-MIT.txt',
        'go-gameserver/LICENSES/puddle-MIT.txt',
        'go-gameserver/LICENSES/go-BSD-3-Clause.txt'
    )
    $missingLicenseFiles = @(
        $requiredLicenseFiles | Where-Object { $_ -notin $publicationFiles }
    )
    $requiredLicenseMarkers = [ordered]@{
        'LICENSE.md'                                          = 'Copyright (c) 2026 mrtats'
        'go-gameserver/LICENSES/project-MIT.txt'               = 'Copyright (c) 2026 mrtats'
        'go-gameserver/LICENSES/predecessor-MIT.txt'           = 'Copyright (c) 2017 Mateo M.'
        'go-gameserver/LICENSES/pgx-MIT.txt'                   = 'Copyright (c) 2013-2021 Jack Christensen'
        'go-gameserver/LICENSES/pgpassfile-MIT.txt'            = 'Copyright (c) 2019 Jack Christensen'
        'go-gameserver/LICENSES/pgservicefile-MIT.txt'         = 'Copyright (c) 2020 Jack Christensen'
        'go-gameserver/LICENSES/puddle-MIT.txt'                = 'Copyright (c) 2018 Jack Christensen'
        'go-gameserver/LICENSES/go-BSD-3-Clause.txt'           = 'Copyright 2009 The Go Authors.'
    }
    $invalidLicenseFiles = @()
    foreach ($entry in $requiredLicenseMarkers.GetEnumerator()) {
        if (Test-Path -LiteralPath $entry.Key) {
            $licenseText = Get-Content -LiteralPath $entry.Key -Raw
            if (-not $licenseText.Contains($entry.Value)) {
                $invalidLicenseFiles += "$($entry.Key) (missing $($entry.Value))"
            }
        }
    }

    $licenseManifestPath = 'go-gameserver/LICENSES/README.md'
    $licenseManifest = Get-Content -LiteralPath $licenseManifestPath -Raw
    $goModule = & go -C go-gameserver mod edit -json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) {
        throw 'go mod edit failed'
    }
    $missingModuleNotices = @()
    foreach ($requirement in $goModule.Require) {
        $manifestEntry = '| `{0}` | `{1}` |' -f $requirement.Path, $requirement.Version
        if (-not $licenseManifest.Contains($manifestEntry)) {
            $missingModuleNotices += "$($requirement.Path) $($requirement.Version)"
        }
    }

    $dockerfile = Get-Content -LiteralPath 'go-gameserver/Dockerfile' -Raw
    $dockerOmitsLicenses = $dockerfile -notmatch '(?m)^COPY LICENSES /licenses\r?$'

    $patchPolicyMarker = 'The patch service may only distribute project-authored files'
    $patchPolicyDocuments = @(
        'tools/launcher/README.md',
        'web_db/README.md'
    )
    $missingPatchPolicies = @(
        $patchPolicyDocuments | Where-Object {
            -not (Get-Content -LiteralPath $_ -Raw).Contains($patchPolicyMarker)
        }
    )

    $riskyDistributionPatterns = @(
        ('complete ' + 'client files'),
        ('c' + 'shell[.]dll')
    )
    $riskyDistributionGuidance = @()
    foreach ($pattern in $riskyDistributionPatterns) {
        foreach ($path in $publicationFiles) {
            if ([IO.Path]::GetExtension($path).ToLowerInvariant() -in $blockedExtensions) {
                continue
            }
            $matches = @(Select-String -LiteralPath $path -Pattern $pattern)
            foreach ($match in $matches) {
                $riskyDistributionGuidance += "$path`:$($match.LineNumber):$($match.Line.Trim())"
            }
        }
    }

    $markerPatterns = @(
        ('F' + 'UN_[0-9A-Fa-f]+'),
        ('Ghi' + 'dra'),
        ('Character' + 'Data[.]dat'),
        ('ExItem' + 'Data[.]dat')
    )
    $researchMarkers = @()
    foreach ($pattern in $markerPatterns) {
        foreach ($path in $publicationFiles) {
            if ([IO.Path]::GetExtension($path).ToLowerInvariant() -in $blockedExtensions) {
                continue
            }
            $matches = @(Select-String -LiteralPath $path -Pattern $pattern)
            foreach ($match in $matches) {
                $researchMarkers += "$path`:$($match.LineNumber):$($match.Line.Trim())"
            }
        }
    }

    $brandingPatterns = @(
        ('Wolf' + '[\s._-]*' + 'Team')
    )
    $brandingReferences = @()
    foreach ($pattern in $brandingPatterns) {
        foreach ($path in $publicationFiles) {
            if ([IO.Path]::GetExtension($path).ToLowerInvariant() -in $blockedExtensions) {
                continue
            }
            $matches = @(Select-String -LiteralPath $path -Pattern $pattern)
            foreach ($match in $matches) {
                $brandingReferences += "$path`:$($match.LineNumber):$($match.Line.Trim())"
            }
        }
    }

    $personalPathPatterns = @(
        ('[A-Za-z]:' + '[\\/]+Users[\\/]+[^\\/\s"''<>|]+[\\/]+'),
        ('/' + 'home/[^/\s"''<>|]+/'),
        ('/' + 'Users/[^/\s"''<>|]+/')
    )
    $personalPathReferences = @()
    foreach ($pattern in $personalPathPatterns) {
        foreach ($path in $publicationFiles) {
            if ([IO.Path]::GetExtension($path).ToLowerInvariant() -in $blockedExtensions) {
                continue
            }
            $matches = @(Select-String -LiteralPath $path -Pattern $pattern)
            foreach ($match in $matches) {
                $personalPathReferences += "$path`:$($match.LineNumber):$($match.Line.Trim())"
            }
        }
    }

    $ipLiteralPattern = '(?<![A-Za-z0-9_:])(?:\d{1,3}(?:\.\d{1,3}){3}|(?:[0-9A-Fa-f]{0,4}:){2,}[0-9A-Fa-f]{0,4})(?![A-Za-z0-9_:])'
    $nonAddressVersionLiterals = @('1.0.0.0', '6.0.0.0')
    $identifiableAddressReferences = @()
    foreach ($path in $publicationFiles) {
        if ([IO.Path]::GetExtension($path).ToLowerInvariant() -in $blockedExtensions) {
            continue
        }
        $lineMatches = @(Select-String -LiteralPath $path -Pattern $ipLiteralPattern)
        foreach ($lineMatch in $lineMatches) {
            foreach ($literalMatch in [regex]::Matches($lineMatch.Line, $ipLiteralPattern)) {
                if ($literalMatch.Value -in $nonAddressVersionLiterals) {
                    continue
                }
                $address = $null
                if ([Net.IPAddress]::TryParse($literalMatch.Value, [ref]$address) -and
                    -not (Test-IsSafePublicationAddress -Address $address)) {
                    $identifiableAddressReferences += (
                        "$path`:$($lineMatch.LineNumber):$($literalMatch.Value)"
                    )
                }
            }
        }
    }

    $failures = @()
    if ($blockedFiles.Count -gt 0) {
        $failures += "Blocked binary/data files:`n  $($blockedFiles -join "`n  ")"
    }
    if ($clientPayloads.Count -gt 0) {
        $failures += "Tracked client payloads:`n  $($clientPayloads -join "`n  ")"
    }
    if ($missingLicenseFiles.Count -gt 0) {
        $failures += "Missing required license files:`n  $($missingLicenseFiles -join "`n  ")"
    }
    if ($invalidLicenseFiles.Count -gt 0) {
        $failures += "Invalid required license files:`n  $($invalidLicenseFiles -join "`n  ")"
    }
    if ($missingModuleNotices.Count -gt 0) {
        $failures += "Go modules missing from the license manifest:`n  $($missingModuleNotices -join "`n  ")"
    }
    if ($dockerOmitsLicenses) {
        $failures += 'The final server image does not copy LICENSES to /licenses.'
    }
    if ($missingPatchPolicies.Count -gt 0) {
        $failures += "Patch documentation missing the redistribution policy:`n  $($missingPatchPolicies -join "`n  ")"
    }
    if ($riskyDistributionGuidance.Count -gt 0) {
        $failures += "Risky proprietary-client distribution guidance:`n  $($riskyDistributionGuidance -join "`n  ")"
    }
    if ($researchMarkers.Count -gt 0) {
        $failures += "Detailed private-research markers:`n  $($researchMarkers -join "`n  ")"
    }
    if ($brandingReferences.Count -gt 0) {
        $failures += "Product-brand references:`n  $($brandingReferences -join "`n  ")"
    }
    if ($personalPathReferences.Count -gt 0) {
        $failures += "Personal filesystem paths:`n  $($personalPathReferences -join "`n  ")"
    }
    if ($identifiableAddressReferences.Count -gt 0) {
        $failures += "Publicly routable IP-address literals:`n  $($identifiableAddressReferences -join "`n  ")"
    }

    if ($failures.Count -gt 0) {
        Write-Error ($failures -join "`n`n")
        exit 1
    }

    Write-Host "Source-release check passed for $($publicationFiles.Count) files."
} finally {
    Pop-Location
}
