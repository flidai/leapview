import importlib.util
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('nix_ci_environment', ROOT / 'scripts/export_nix_ci_environment.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class EnvironmentTests(unittest.TestCase):
    def environment(self):
        return {
            'PATH': '/nix/store/tools/bin:/usr/bin',
            'GOTOOLCHAIN': 'local',
            'CC': 'gcc', 'CXX': 'g++', 'AR': 'ar', 'LD': 'ld',
            'NIX_CC': '/nix/store/compiler', 'NIX_BINTOOLS': '/nix/store/binutils',
            'FONTCONFIG_FILE': '/nix/store/fonts/fonts.conf',
            'PLAYWRIGHT_BROWSERS_PATH': '/nix/store/browsers',
            'LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION': '1.61.1',
            'NIX_CFLAGS_COMPILE': '-isystem /nix/store/headers/include',
            'LD_LIBRARY_PATH': '/nix/store/runtime/lib',
            'GH_TOKEN': 'must-never-be-exported',
            'HOME': '/private/home',
            'NODE_OPTIONS': '--require /private/script',
        }

    def test_exports_compiler_and_browser_environment_without_runner_secrets(self):
        env, path = m.render(self.environment(), 'validation')
        self.assertEqual(path, '/nix/store/tools/bin:/usr/bin\n')
        for name in ['GOTOOLCHAIN', 'FONTCONFIG_FILE', 'PLAYWRIGHT_BROWSERS_PATH',
                     'LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION', 'NIX_CFLAGS_COMPILE', 'LD_LIBRARY_PATH']:
            self.assertIn(f'{name}={self.environment()[name]}\n', env)
        for forbidden in ['must-never-be-exported', '/private/']:
            self.assertNotIn(forbidden, env)
        self.assertFalse(any(line.startswith('PATH=') for line in env.splitlines()))
        self.assertIn('PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1\n', env)

    def test_rejects_incomplete_or_non_nix_environment(self):
        for name in ['CC', 'CXX', 'AR', 'LD', 'NIX_CC', 'NIX_BINTOOLS', 'PATH', 'GOTOOLCHAIN', 'FONTCONFIG_FILE', 'PLAYWRIGHT_BROWSERS_PATH',
                     'LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION']:
            env = self.environment()
            del env[name]
            with self.subTest(name=name), self.assertRaises(ValueError):
                m.render(env, 'validation')
        for name in ['FONTCONFIG_FILE', 'PLAYWRIGHT_BROWSERS_PATH']:
            env = self.environment() | {name: '/tmp/unlocked'}
            with self.subTest(name=name), self.assertRaises(ValueError):
                m.render(env, 'validation')
        with self.assertRaises(ValueError):
            m.render(self.environment() | {'GOTOOLCHAIN': 'auto'}, 'validation')

    def test_rejects_environment_file_injection(self):
        for name in ['PATH', 'NIX_CFLAGS_COMPILE', 'LD_LIBRARY_PATH']:
            for suffix in ['\nGH_TOKEN=attack', '\rGH_TOKEN=attack', '\x00']:
                env = self.environment()
                env[name] += suffix
                with self.subTest(name=name, suffix=suffix), self.assertRaises(ValueError):
                    m.render(env, 'validation')

    def test_validation_precedes_writing_either_file(self):
        with tempfile.TemporaryDirectory() as directory:
            env_path = pathlib.Path(directory) / 'env'
            path_path = pathlib.Path(directory) / 'path'
            env_path.write_text('original\n')
            path_path.write_text('original\n')
            with self.assertRaises(ValueError):
                m.export(self.environment() | {'PATH': 'invalid\npath'}, env_path, path_path, 'validation')
            self.assertEqual(env_path.read_text(), 'original\n')
            self.assertEqual(path_path.read_text(), 'original\n')

    def test_appends_without_overwriting_prior_action_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            env_path = pathlib.Path(directory) / 'env'
            path_path = pathlib.Path(directory) / 'path'
            env_path.write_text('PRIOR=kept\n')
            m.export(self.environment(), env_path, path_path, 'validation')
            self.assertTrue(env_path.read_text().startswith('PRIOR=kept\n'))
            self.assertEqual(path_path.read_text(), '/nix/store/tools/bin:/usr/bin\n')

    def test_orchestration_exports_only_locked_go_and_path(self):
        environment = {'PATH': '/nix/store/go/bin:/usr/bin', 'GOTOOLCHAIN': 'local'}
        env, path = m.render(environment, 'orchestration')
        self.assertEqual(env, 'GOTOOLCHAIN=local\n')
        self.assertEqual(path, environment['PATH'] + '\n')
        # Inherited browser/CGO settings must not extend this profile's contract.
        env, _ = m.render(self.environment(), 'orchestration')
        self.assertEqual(env, 'GOTOOLCHAIN=local\n')

    def test_orchestration_rejects_unlocked_or_incomplete_inputs(self):
        environment = {'PATH': '/nix/store/go/bin:/usr/bin', 'GOTOOLCHAIN': 'local'}
        for name in environment:
            incomplete = environment.copy()
            del incomplete[name]
            with self.subTest(name=name), self.assertRaises(ValueError):
                m.render(incomplete, 'orchestration')
        for change in [{'GOTOOLCHAIN': 'auto'}, {'PATH': '/usr/bin'},
                       {'PATH': '/nix/store/go/bin\nEVIL=value'}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                m.render(environment | change, 'orchestration')
        with self.assertRaises(ValueError):
            m.render(environment, 'unknown')
