import importlib.util
from pathlib import Path
import shutil
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('version_probe', ROOT / 'nix/check-extension-version.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class ExtensionVersion(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.source = Path(self.temp.name) / 'extension.cpp'
        self.header = Path(self.temp.name) / 'extension.hpp'
        self.header.write_text('class QuackExtension { public: std::string Version() const override; };')
        self.compiler = shutil.which('c++')
        self.assertIsNotNone(self.compiler, 'selected C++ compiler is required')
        self.revision = '40de7badae4193c29d9c0834473fb76acc6c51e6'

    def check(self, name='QuackExtension', macro='EXT_VERSION_QUACK'):
        probe.check(self.source, self.header, self.compiler, name, macro, self.revision)

    def test_compiles_real_method_with_only_engine_selected_macro(self):
        self.source.write_text('std::string QuackExtension::Version() const {\n#ifdef EXT_VERSION_QUACK\nreturn EXT_VERSION_QUACK;\n#else\nreturn "";\n#endif\n}')
        self.check()
        self.source.write_text(self.source.read_text().replace('EXT_VERSION_QUACK', 'EXT_VERSION_RPC'))
        with self.assertRaisesRegex(ValueError, 'compiled extension revision differs'):
            self.check()

    def test_macro_free_loader_rejects_inline_body_and_accepts_out_of_line_definition(self):
        body = '\n#ifdef EXT_VERSION_POSTGRES_SCANNER\nreturn EXT_VERSION_POSTGRES_SCANNER;\n#else\nreturn "";\n#endif\n'
        self.header.write_text('class PostgresScannerExtension { public: std::string Version() const override {' + body + '} };')
        self.source.write_text('// Version was incorrectly inline in the shared header.\n')
        with self.assertRaisesRegex(ValueError, 'compiled extension revision differs'):
            self.check('PostgresScannerExtension', 'EXT_VERSION_POSTGRES_SCANNER')
        self.header.write_text('class PostgresScannerExtension { public: std::string Version() const override; };')
        self.source.write_text('std::string PostgresScannerExtension::Version() const {' + body + '}')
        self.check('PostgresScannerExtension', 'EXT_VERSION_POSTGRES_SCANNER')
        self.header.write_text('class PostgresScannerExtension { public: void Load(); };')
        with self.assertRaisesRegex(ValueError, 'declare and define exactly one'):
            self.check('PostgresScannerExtension', 'EXT_VERSION_POSTGRES_SCANNER')

    def test_substituted_literal_revision_is_rejected(self):
        self.source.write_text('std::string QuackExtension::Version() const { return "substituted"; }')
        with self.assertRaisesRegex(ValueError, 'compiled extension revision differs'):
            self.check()


if __name__ == '__main__':
    unittest.main()
