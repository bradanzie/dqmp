
# DQMP - Decentralized QUIC Mesh Protocol - Guide Utilisateur (v0.1.0-dev)

Bienvenue dans le guide utilisateur de DQMP, un protocole de réseau maillé décentralisé axé sur la performance, la résilience et l'efficacité énergétique. Ce document décrit comment compiler, configurer, exécuter un nœud DQMP (`dqmpd`) et interagir avec lui à l'aide de l'outil en ligne de commande `dqmpctl`.

## Table des Matières

1.  [Compilation](#compilation)
2.  [Configuration d'un Nœud (`dqmpd`)](#configuration-dun-nœud-dqmpd)
    *   [Structure du Fichier de Configuration](#structure-du-fichier-de-configuration)
    *   [Exemples de Configuration](#exemples-de-configuration)
3.  [Exécution d'un Nœud (`dqmpd`)](#exécution-dun-nœud-dqmpd)
4.  [Utilisation de l'Outil CLI (`dqmpctl`)](#utilisation-de-loutil-cli-dqmpctl)
    *   [Options Globales](#options-globales)
    *   [Commandes de Base](#commandes-de-base)
    *   [Commandes de Données](#commandes-de-données)
    *   [Commandes d'Énergie](#commandes-dénergie)
5.  [Mise en Place d'un Réseau Local (Test)](#mise-en-place-dun-réseau-local-test)

---

## 1. Compilation

Assurez-vous d'avoir [Go](https://go.dev/doc/install) (version 1.21 ou supérieure recommandée) installé sur votre système.

1.  **Cloner le Dépôt (si applicable) :**
    ```bash
    # git clone <url_du_depot>
    # cd dqmp-project
    ```

2.  **Compiler les Binaires :** Utilisez le `Makefile` fourni pour compiler le démon `dqmpd` et l'outil CLI `dqmpctl`.
    ```bash
    make build
    ```
    Les binaires compilés se trouveront dans le répertoire `bin/`.

3.  **(Optionnel) Ajouter `bin/` à votre PATH :** Pour pouvoir exécuter `dqmpd` et `dqmpctl` depuis n'importe où.
    ```bash
    # Exemple pour bash/zsh
    # export PATH=$PATH:$(pwd)/bin
    ```

---

## 2. Configuration d'un Nœud (`dqmpd`)

Chaque nœud DQMP est configuré via un fichier YAML. Un fichier d'exemple `config/default.yaml` est fourni.

### Structure du Fichier de Configuration

Le fichier de configuration est divisé en plusieurs sections :

*   **`network`**: Paramètres liés au réseau DQMP (QUIC).
    *   `listen`: Adresse et port d'écoute pour les connexions DQMP (ex: `"127.0.0.1:4242"` ou `":4242"` pour écouter sur toutes les interfaces). **Requis.**
    *   `multipath`: (Fonctionnalité future) Options pour MP-QUIC.
        *   `enabled`: `true` ou `false`.
        *   `max_paths`: Nombre maximum de chemins.
*   **`logging`**: Configuration des logs.
    *   `level`: Niveau de log (`debug`, `info`, `warn`, `error`). Défaut: `info`.
*   **`api`**: Configuration de l'API REST de contrôle.
    *   `enabled`: `true` pour activer l'API, `false` pour la désactiver. Défaut: `true`.
    *   `listen`: Adresse et port d'écoute pour l'API REST (ex: `"127.0.0.1:8003"` ou `":8003"`). Doit être différent du port `network.listen`. Défaut: `:8002` (Attention, vos exemples utilisaient 8003, 8004...).
*   **`data`**: Configuration du stockage local.
    *   `directory`: Chemin vers le répertoire où stocker la base de données locale (ex: `"./dqmp_node_data"`). Si non absolu, relatif au répertoire d'exécution de `dqmpd`. Le code actuel le rend unique en ajoutant le port DQMP si le chemin par défaut est utilisé.
*   **`discovery`**: Paramètres pour la découverte de pairs (Libp2p, DHT, mDNS).
    *   `listen_addrs`: Liste des [Multiadresses](https://docs.libp2p.io/concepts/fundamentals/addressing/) sur lesquelles le service Libp2p écoutera (ex: `["/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"]`). Le port `0` signifie un port choisi automatiquement.
    *   `bootstrap_peers`: Liste des Multiadresses complètes (incluant `/p2p/PEER_ID`) des nœuds d'amorçage Libp2p. Laisser vide (`[]`) ou omettre pour utiliser les bootstrap peers publics par défaut de libp2p. **Pour les tests locaux, spécifiez l'adresse TCP loopback du premier nœud.**
    *   `rendezvous`: Une chaîne de caractères secrète partagée par tous les nœuds DQMP pour se trouver via le DHT (ex: `"dqmp-network-v1.0"`). **Requis et doit être identique pour tous les nœuds du même réseau.**
    *   `identity_path`: Chemin vers le fichier contenant la clé privée Libp2p du nœud (ex: `"dqmp_identity.key"`). S'il n'existe pas, il sera créé. Le code actuel le rend unique en ajoutant le port DQMP si le chemin par défaut est utilisé.
    *   `discovery_interval`: Intervalle de temps pour la recherche périodique de pairs via DHT (ex: `"1m"`, `"30s"`). Défaut: `1m`.
*   **`energy`**: (Fonctionnalité future/simulée) Paramètres énergétiques.
    *   `policy`: Politique énergétique (ex: `"balanced"`).
    *   `thresholds`: Seuils de batterie.
        *   `critical`: Niveau critique (%).
        *   `warning`: Niveau d'avertissement (%).

### Exemples de Configuration

Voir les fichiers `config/default.yaml`, `config/node1.yaml`, `config/node2.yaml`, etc., fournis dans le projet pour des exemples concrets. **Adaptez les ports et les chemins selon vos besoins.**

---

## 3. Exécution d'un Nœud (`dqmpd`)

Pour lancer un nœud DQMP, utilisez le binaire `dqmpd` en spécifiant le fichier de configuration :

```bash
# Utiliser le chemin de configuration par défaut (config/default.yaml)
./bin/dqmpd

# Spécifier un fichier de configuration
./bin/dqmpd --config config/node1.yaml
# ou forme courte
./bin/dqmpd -c config/node2.yaml
```

Le nœud démarrera, initialisera ses composants (stockage, réseau, découverte, API...) et affichera des logs indiquant son état. Pour arrêter le nœud proprement, appuyez sur `Ctrl+C`.

---

## 4. Utilisation de l'Outil CLI (`dqmpctl`)

`dqmpctl` est l'outil en ligne de commande pour interagir avec l'API REST d'un nœud `dqmpd` en cours d'exécution.

### Options Globales

*   `-t, --target string`: Spécifie l'URL de base de l'API REST du nœud cible. Requis pour la plupart des commandes.
    *   Défaut : `http://127.0.0.1:8080` (Attention, le port API par défaut de `dqmpd` est `:8002` dans la config par défaut, et vos exemples utilisent `:8003`, `:8004`...). **Assurez-vous de cibler le bon port API du nœud.**
    *   Exemple : `-t http://127.0.0.1:8003`

### Commandes de Base

*   **`dqmpctl help`**: Affiche l'aide générale.
*   **`dqmpctl help <commande>`**: Affiche l'aide pour une commande spécifique (ex: `dqmpctl help data`).
*   **`dqmpctl status`**: Récupère et affiche le statut général du nœud cible.
    ```bash
    ./bin/dqmpctl -t http://127.0.0.1:8003 status
    ```
*   **`dqmpctl peers`**: Liste les pairs connus par le nœud cible, leur état de connexion DQMP et leur adresse DQMP si connectés.
    ```bash
    ./bin/dqmpctl -t http://127.0.0.1:8003 peers
    ```
*   **`dqmpctl ping <adresse_dqmp>`**: Envoie un PING applicatif via QUIC DQMP directement à l'adresse DQMP d'un nœud (ex: `"127.0.0.1:4242"`) et attend un PONG. Utile pour tester la connectivité QUIC de base. *Note : Ne passe pas par l'API REST.*
    ```bash
    ./bin/dqmpctl ping 127.0.0.1:4242
    ```

### Commandes de Données (`dqmpctl data ...`)

Ces commandes interagissent avec le stockage clé-valeur du nœud cible via l'API REST.

*   **`dqmpctl data list`**: Liste toutes les clés stockées sur le nœud cible.
    ```bash
    ./bin/dqmpctl -t http://127.0.0.1:8003 data list
    ```
*   **`dqmpctl data get <clé>`**: Récupère la valeur associée à `<clé>`.
    *   `-o, --output <fichier>`: (Optionnel) Écrit la valeur dans un fichier au lieu de l'afficher sur la sortie standard.
    ```bash
    # Afficher la valeur
    ./bin/dqmpctl -t http://127.0.0.1:8003 data get ma_cle

    # Écrire la valeur dans un fichier
    ./bin/dqmpctl -t http://127.0.0.1:8003 data get ma_cle -o valeur.txt
    ```
*   **`dqmpctl data put <clé> [valeur]`**: Stocke (ou met à jour) la `<valeur>` pour la `<clé>` donnée. La `<valeur>` peut être fournie directement en argument ou lue depuis un fichier. Les données stockées seront aussi (tentées d'être) répliquées vers d'autres pairs connectés.
    *   `-i, --input <fichier>`: Lit la valeur depuis le `<fichier>` spécifié au lieu de l'argument.
    ```bash
    # Valeur en argument
    ./bin/dqmpctl -t http://127.0.0.1:8003 data put ma_cle "Ma super valeur"

    # Valeur depuis un fichier
    echo "Contenu depuis fichier" > data.bin
    ./bin/dqmpctl -t http://127.0.0.1:8003 data put autre_cle -i data.bin
    rm data.bin
    ```
*   **`dqmpctl data delete <clé>`**: Supprime la paire clé-valeur associée à `<clé>`.
    ```bash
    ./bin/dqmpctl -t http://127.0.0.1:8003 data delete ma_cle
    ```

### Commandes d'Énergie (`dqmpctl energy ...`)

Ces commandes interagissent avec le module de surveillance énergétique (actuellement simulé).

*   **`dqmpctl energy status`**: Affiche le statut énergétique actuel rapporté par le nœud cible (source, niveau batterie, consommation, EcoScore...).
    ```bash
    ./bin/dqmpctl -t http://127.0.0.1:8003 energy status
    ```

---

## 5. Mise en Place d'un Réseau Local (Test)

Pour tester les interactions entre plusieurs nœuds sur votre machine :

1.  **Créez des Fichiers de Configuration Distincts :** Créez `config/node1.yaml`, `config/node2.yaml`, `config/node3.yaml`, etc.
2.  **Attribuez des Ports Uniques :** Assurez-vous que chaque fichier de configuration utilise des ports différents pour `network.listen` (ex: 4242, 4243, 4244) et `api.listen` (ex: 8003, 8004, 8005).
3.  **Configurez les Répertoires/Identités :** Assurez-vous que `data.directory` et `discovery.identity_path` sont uniques pour chaque nœud (le code actuel aide en ajoutant le port, mais vérifiez).
4.  **Choisissez une Stratégie de Bootstrap :**
    *   **Bootstrap Local Explicite :** Lancez Node 1. Récupérez son adresse multiaddr Libp2p TCP loopback (ex: `/ip4/127.0.0.1/tcp/PORT1/p2p/ID1`). Configurez Node 2, Node 3, etc., pour utiliser cette adresse dans leur `discovery.bootstrap_peers`. C'est le plus fiable en local.
    *   **mDNS Uniquement :** Laissez `discovery.bootstrap_peers` vide (`[]`) dans toutes les configurations. Les nœuds devraient se trouver via mDNS s'ils sont sur le même segment réseau.
    *   **Bootstrap Public (Test Connectivité Externe) :** Laissez `discovery.bootstrap_peers` vide (`[]`). Les nœuds tenteront de contacter les serveurs publics. Cela échouera probablement si votre réseau est restrictif.
5.  **Lancez les Nœuds :** Ouvrez un terminal pour chaque nœud et lancez `dqmpd` avec son fichier de configuration respectif.
    ```bash
    # Terminal 1
    ./bin/dqmpd -c config/node1.yaml
    # Terminal 2
    ./bin/dqmpd -c config/node2.yaml
    # Terminal 3
    ./bin/dqmpd -c config/node3.yaml
    # ...
    ```
6.  **Vérifiez la Connexion :** Utilisez `dqmpctl -t <api_addr> peers` sur chaque nœud pour voir s'ils se découvrent et se connectent les uns aux autres.
7.  **Testez la Réplication :** Utilisez `dqmpctl data put` sur un nœud et vérifiez avec `dqmpctl data get` si la donnée apparaît sur les autres nœuds connectés (selon la logique de sélection par EcoScore).

---

Ce document sera mis à jour au fur et à mesure de l'ajout de nouvelles fonctionnalités. N'hésitez pas à utiliser l'option `--help` des commandes pour plus de détails.