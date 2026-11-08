# Windows runtime evidence for a local preview

This is an evidence record for one local development preview, not permission
to redistribute it. Windows CI uses its runner's system DLLs and is not covered
by this record. The app remains subject to the Kiwix source and licensing review.

On 2026-11-05, the three C++ runtime DLLs in the locally assembled Windows
preview built from toolkit commit `717774168743454f2410f93180327e9b8dde3f1d`
matched the corresponding files extracted from a local `vc_redist.x64.exe`.
A fresh download through Microsoft's documented
[latest x64 redistributable link](https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist?view=msvc-170)
matched that installer byte for byte. The link is a moving target and may serve
a different version later. The file downloaded during this check reported
version 14.51.36247.0 in its version resource. Its locally computed SHA-256 was
`843068991daaa1f73ad9f6239bce4d0f6a07a51f18c37ea2a867e9beca71295c`.
The observed HTTPS download redirected to a `download.visualstudio.microsoft.com`
URL containing that same digest; that is not an independently published checksum.

The installer's payload cabinet was found verbatim inside the downloaded EXE.
The x64 runtime cabinet extracted from it had SHA-256
`6e8ee73933678c55973e83366e48e948b394c7a36743a36b7224c95de979b13f`.
Extracting the following members from that cabinet produced hashes matching the
DLLs in the preview:

| Runtime file | Locally computed SHA-256 |
| --- | --- |
| `msvcp140.dll` | `7c26614e1d733892c2deac7e245ce115504b1d80592dd0a01b08e3e5a55f89ca` |
| `vcruntime140.dll` | `d1f4225df2cd877dbf130d5668a021dce3f94118455ff5ec952061c30afc9ce7` |
| `vcruntime140_1.dll` | `a7146c08f89fe5b04541ab507cdb59ff7b44534d4ba3c668a426c6450a03434e` |

The first `osslsigncode` verification could not build a certificate chain using
the Linux system trust store. A second check used temporary, explicitly selected
Microsoft roots downloaded from the [Microsoft PKI repository](https://www.microsoft.com/pkiops/docs/repository.htm),
without changing the system trust store. Their SHA-256 fingerprints matched
Microsoft's published [independent audit report](https://www.microsoft.com/pkiops/docs/Content/seals/microsoft%20trp%20wtca%20indp%20acct%20opinion%20and%20mgmt%20assertion%20june%202023%20-%20final.pdf):
`847df6a78497943f27fc72eb93f9a637320a02b561d0a91b09e87a7807ed7c61`
for Microsoft Root Certificate Authority 2011, and
`65af95f4be86847344634282f941b2e605063ef0c8542f014ca088d182109e4f`
for Microsoft Time Stamp Root Certificate Authority 2014. With those anchors,
`osslsigncode` reported matching file digests, a valid signer chain and successful
signature CRL verification. It displayed a timestamp but also reported that a
timestamp was not available for verification in that Linux check.

A subsequent Windows runner audit in the previous repository
downloaded the exact installer from Microsoft and checked its SHA-256. Windows
`Get-AuthenticodeSignature` reported `Valid` and identified a Microsoft signer
and timestamp certificate. Windows SDK SignTool `verify /pa /tw /v` verified the
signature and timestamp with zero warnings or errors. This checks that exact
installer under that runner's trust configuration; it does not validate every DLL
individually, guarantee other machines' trust state, or clear redistribution.
Repeat appropriate signature, timestamp and revocation checks on release inputs.

Microsoft's [redistribution guidance](https://learn.microsoft.com/en-us/cpp/windows/redistributing-visual-cpp-files?view=msvc-170)
says that redistribution is subject to Visual Studio licensing and its terms;
it also discusses application-local deployment and servicing concerns. Confirm
the operator's entitlement, applicable version-specific license and notices,
runtime compatibility, and security servicing plan before redistribution.
The app-folder hashes alone do not resolve those questions. No installer or
runtime binary is added to this source repository by this evidence record.
