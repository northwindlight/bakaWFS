import contextlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('cloud_ui', Path(__file__).resolve().parents[1]/'scripts/package-cloud-ui.py')
cloud_ui = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cloud_ui)

class PackagingSafety(unittest.TestCase):
    def test_refuses_to_clear_unmanaged_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);source=root/'source';source.mkdir();(source/'index.html').write_text('<p>site</p>')
            output=root/'important';output.mkdir();(output/'keep.txt').write_text('keep')
            with self.assertRaises(ValueError):cloud_ui.package(source,output,'https://api.example.test')
            self.assertEqual((output/'keep.txt').read_text(),'keep')

    def test_rebuilds_only_owned_outputs(self):
        with tempfile.TemporaryDirectory() as tmp,contextlib.redirect_stdout(io.StringIO()):
            root=Path(tmp);source=root/'source';source.mkdir();(source/'index.html').write_text('<script src="./main.js"></script>');(source/'main.js').write_text('const value = 1;')
            output=root/'build';cloud_ui.package(source,output,'https://api.example.test');(source/'main.js').write_text('const value = 2;');cloud_ui.package(source,output,'https://api.example.test')
            self.assertTrue(any('value = 2' in p.read_text() for p in output.glob('main.*.js')))
            (output/'keep-private.txt').write_text('keep')
            with self.assertRaises(ValueError):cloud_ui.package(source,output,'https://api.example.test')
            self.assertEqual((output/'keep-private.txt').read_text(),'keep')

if __name__=='__main__':unittest.main()
