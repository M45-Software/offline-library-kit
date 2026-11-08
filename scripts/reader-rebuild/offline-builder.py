import csv, hashlib, os, sys
from pathlib import Path
scratch=Path(os.environ['OLK_REBUILD_DIR'])
source_manifest=scratch/'INPUT-SHA256SUMS'
expected={}
for line in source_manifest.read_text().splitlines():
 sha, name = line.split('  ', 1)
 if name.startswith('kiwix-archives/'):
  expected[Path(name).name]=sha
sys.path.insert(0,str(scratch/'tools'))
from kiwixbuild import buildenv
from kiwixbuild.dependencies.base import GitClone
from kiwixbuild.dependencies.kiwix_tools import KiwixTools
from kiwixbuild.utils import SkipCommand, StopBuild
import kiwixbuild

def offline_git(self):
 allowed={'libzim_release','libkiwix_release','kiwix-tools_release'}
 if Path(self.git_path).name not in allowed or not Path(self.git_path).is_dir():
  raise StopBuild('Unexpected or missing captured Git source: '+self.git_path)
 print('  Using captured source tree:',self.git_path,flush=True)

def offline_download(self,what,where=None):
 file=Path(where or self.archive_dir)/what.name
 if what.name not in expected or not file.is_file():
  raise StopBuild('Missing captured archive: '+what.name)
 h=hashlib.sha256()
 with file.open('rb') as stream:
  for b in iter(lambda:stream.read(1024*1024),b''):h.update(b)
 actual=h.hexdigest()
 if actual!=expected[what.name] or (what.sha256 and actual!=what.sha256):
  raise StopBuild('Archive hash mismatch: '+what.name)
 raise SkipCommand('verified captured archive')

GitClone.prepare=offline_git
buildenv.NeutralEnv.download=offline_download
original=KiwixTools.Builder.configure_options.fget
def tools_options(self):
 yield from original(self)
 yield '-Dcpp_link_args=-Wl,--no-fatal-warnings'
KiwixTools.Builder.configure_options=property(tools_options)
# The build is allowed to finish only if all emitted warnings match the four
# known glibc static-link diagnostics. Compiler/linker nonzero exits still fail.
from link_warning_policy import check_log
original_compile=KiwixTools.Builder._compile
def checked_compile(self, context):
 try:
  original_compile(self, context)
 except SkipCommand:
  raise StopBuild('Strict warning check requires a fresh Kiwix tools compile')
 try:
  warnings=check_log(context.log_file)
 except (OSError, UnicodeError, ValueError) as exc:
  raise StopBuild('Kiwix tools warning verification failed: '+str(exc))
 print('  Accepted known static glibc warning lines:',len(warnings),flush=True)
KiwixTools.Builder._compile=checked_compile
sys.argv=['kiwix-build','kiwix-tools','--working-dir',str(scratch),'--config','native_static','--make-release','--dont-install-packages','--hide-progress']
kiwixbuild.main()
