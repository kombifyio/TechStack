#requires -Version 7.5
param(
    [Parameter(Mandatory = $true)][string]$OutputDirectory,
    [string]$ArchivePath = ""
)

$ErrorActionPreference = "Stop"
# Same PostgreSQL release as embedded-postgres v1.34.0's V16 runtime pin.
$version = "16.9.0"
$archiveSHA256 = "f6c6499661eb7064d2be60833e0e53621e42a880309ae5b82aa0f93eaed02df2"
$archiveUrl = "https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-windows-amd64/$version/embedded-postgres-binaries-windows-amd64-$version.jar"
$temporaryArchive = ""
$temporaryBundle = ""
try {
    if ([string]::IsNullOrWhiteSpace($ArchivePath)) {
        $temporaryArchive = [IO.Path]::GetTempFileName()
        $ArchivePath = $temporaryArchive
        Write-Host "Downloading pinned Windows PostgreSQL $version archive."
        Invoke-WebRequest -Uri $archiveUrl -OutFile $ArchivePath -TimeoutSec 120
    }
    if ((Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $archiveSHA256) {
        throw "Windows PostgreSQL archive does not match the pinned SHA-256."
    }

    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    $bundleName = "embedded-postgres-binaries-windows-amd64-$version.txz"
    $bundlePath = Join-Path $OutputDirectory $bundleName
    $temporaryBundle = Join-Path $OutputDirectory (".postgres-" + [Guid]::NewGuid().ToString("N") + ".tmp")
    $zip = [IO.Compression.ZipFile]::OpenRead((Resolve-Path -LiteralPath $ArchivePath).Path)
    try {
        $entry = $zip.GetEntry("postgres-windows-x86_64.txz")
        if ($null -eq $entry) { throw "Pinned archive is missing the Windows PostgreSQL bundle." }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $temporaryBundle)
    } finally {
        $zip.Dispose()
    }
    Move-Item -LiteralPath $temporaryBundle -Destination $bundlePath -Force
    $temporaryBundle = ""
    [ordered]@{
        version = $version
        source = $archiveUrl
        source_sha256 = $archiveSHA256
        archive = $bundleName
        archive_sha256 = (Get-FileHash -LiteralPath $bundlePath -Algorithm SHA256).Hash.ToLowerInvariant()
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputDirectory "bundle.json") -Encoding utf8

    # PostgreSQL REL_16_9/COPYRIGHT; retained beside the binary archive.
    @'
PostgreSQL Database Management System
(formerly known as Postgres, then as Postgres95)

Portions Copyright (c) 1996-2025, PostgreSQL Global Development Group

Portions Copyright (c) 1994, The Regents of the University of California

Permission to use, copy, modify, and distribute this software and its
documentation for any purpose, without fee, and without a written agreement
is hereby granted, provided that the above copyright notice and this
paragraph and the following two paragraphs appear in all copies.

IN NO EVENT SHALL THE UNIVERSITY OF CALIFORNIA BE LIABLE TO ANY PARTY FOR
DIRECT, INDIRECT, SPECIAL, INCIDENTAL, OR CONSEQUENTIAL DAMAGES, INCLUDING
LOST PROFITS, ARISING OUT OF THE USE OF THIS SOFTWARE AND ITS
DOCUMENTATION, EVEN IF THE UNIVERSITY OF CALIFORNIA HAS BEEN ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.

THE UNIVERSITY OF CALIFORNIA SPECIFICALLY DISCLAIMS ANY WARRANTIES,
INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY
AND FITNESS FOR A PARTICULAR PURPOSE.  THE SOFTWARE PROVIDED HEREUNDER IS
ON AN "AS IS" BASIS, AND THE UNIVERSITY OF CALIFORNIA HAS NO OBLIGATIONS TO
PROVIDE MAINTENANCE, SUPPORT, UPDATES, ENHANCEMENTS, OR MODIFICATIONS.
'@ | Set-Content -LiteralPath (Join-Path $OutputDirectory "COPYRIGHT") -Encoding utf8
    Write-Host "Windows PostgreSQL $version bundled at $bundlePath"
} finally {
    foreach ($temporaryFile in @($temporaryArchive, $temporaryBundle)) {
        if ($temporaryFile -and (Test-Path -LiteralPath $temporaryFile)) {
            Remove-Item -LiteralPath $temporaryFile -Force
        }
    }
}
