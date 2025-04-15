import os
import fnmatch
import argparse

DEFAULT_IGNORES = ['.git', 'bundler', 'node_modules', '__pycache__']

def load_ignore_patterns(ignore_file_path):
    patterns = set(DEFAULT_IGNORES)
    if os.path.isfile(ignore_file_path):
        with open(ignore_file_path, 'r') as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith('#'):
                    patterns.add(line)
    return list(patterns)

def should_ignore(path, patterns, base_dir):
    rel_path = os.path.relpath(path, base_dir)
    for pattern in patterns:
        if fnmatch.fnmatch(rel_path, pattern) or fnmatch.fnmatch(os.path.basename(path), pattern):
            return True
    return False

def bundle_files(base_dir, ignore_patterns, output_file):
    with open(output_file, 'w', encoding='utf-8') as out:
        for root, dirs, files in os.walk(base_dir):
            dirs[:] = [d for d in dirs if not should_ignore(os.path.join(root, d), ignore_patterns, base_dir)]
            for file in files:
                file_path = os.path.join(root, file)
                if should_ignore(file_path, ignore_patterns, base_dir):
                    continue
                rel_path = os.path.relpath(file_path, base_dir)
                out.write(f"\n/{rel_path}/\n")
                try:
                    with open(file_path, 'r', encoding='utf-8') as f:
                        out.write(f.read())
                except Exception as e:
                    out.write(f"[Erreur de lecture du fichier: {e}]\n")

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description="Bundler de code source")
    parser.add_argument('--dir', type=str, default=os.path.abspath(os.path.join(os.path.dirname(__file__), '..')),
                        help="Répertoire de base (défaut: un dossier au-dessus du bundler)")
    parser.add_argument('--ignore', type=str, default=os.path.join(os.path.dirname(__file__), '.bundleignore'),
                        help="Fichier .bundleignore (défaut: bundler/.bundleignore)")
    parser.add_argument('--out', type=str, default='bundle.txt',
                        help="Nom du fichier de sortie (défaut: bundle.txt)")

    args = parser.parse_args()

    ignore_patterns = load_ignore_patterns(args.ignore)
    bundle_files(args.dir, ignore_patterns, args.out)
