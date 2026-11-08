# CI-only test: create a temporary self-signed certificate in the ephemeral
# runner's certificate stores. No private key is exported and no TSA is used.
param([Parameter(Mandatory=$true)][string]$Source,
      [Parameter(Mandatory=$true)][string]$Output)
$ErrorActionPreference = 'Stop'
Write-Host 'Locating Windows SDK signing tool'
$kitRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
$tool = Get-ChildItem -Path $kitRoot -Filter signtool.exe -Recurse -File |
    Where-Object { $_.FullName -match '\\x64\\signtool\.exe$' } |
    Sort-Object FullName -Descending | Select-Object -First 1
if (-not $tool) { throw 'Windows SDK signtool.exe not found' }
$cert = $null
$rootStore = $null
try {
    Write-Host 'Creating temporary test certificate'
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=Offline Library CI Test' `
        -CertStoreLocation 'Cert:\CurrentUser\My' -KeyExportPolicy NonExportable `
        -KeyAlgorithm RSA -KeyLength 2048 -NotAfter (Get-Date).AddDays(1)
    $rootStore = [System.Security.Cryptography.X509Certificates.X509Store]::new('Root', 'LocalMachine')
    $rootStore.Open([System.Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
    Write-Host 'Trusting temporary test certificate in isolated runner machine store'
    $rootStore.Add($cert)
    Write-Host 'Signing and verifying app copy'
    $script = Join-Path $env:GITHUB_WORKSPACE 'scripts\sign-windows-preview.py'
    python -u $script $Source $Output --signtool $tool.FullName --thumbprint $cert.Thumbprint
    if ($LASTEXITCODE -ne 0) { throw 'Signing test failed' }
    $original = Get-Content (Join-Path $Source 'build-info.json') -Raw | ConvertFrom-Json
    $signed = Get-Content (Join-Path $Output 'build-info.json') -Raw | ConvertFrom-Json
    if ($signed.pre_signing_shell_sha256 -ne $original.shell_executable_sha256 -or $signed.signing.timestamped) {
        throw 'Signing metadata does not match the original'
    }
    $env:OFFLINE_LIBRARY_SMOKE_APP = Join-Path $Output 'Offline Library.exe'
    Write-Host 'Smoke-testing signed app'
    node test/smoke.cjs
    if ($LASTEXITCODE -ne 0) { throw 'Signed app smoke test failed' }
} finally {
    Write-Host 'Removing temporary test certificate'
    if ($rootStore) {
        if ($cert) { $rootStore.Remove($cert) }
        $rootStore.Close()
    }
    if ($cert) { Remove-Item ("Cert:\CurrentUser\My\" + $cert.Thumbprint) -ErrorAction SilentlyContinue }
}
