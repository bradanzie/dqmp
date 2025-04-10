# dqmp
decentralized protocol

## Comandes utiles

1- build
```bash
make build
```

2- Lancer les Nœuds :
- Terminal 1: `make run CONFIG_FILE=config/node1.yaml`
- Terminal 2: `make run CONFIG_FILE=config/node2.yaml`
- Observer les logs : Vous devriez voir les messages `API: Démarrage du serveur HTTP sur ...` pour chaque nœud. Vous devriez aussi voir les logs de bootstrap et de connexion QUIC comme avant.

3- Ping Pong
```bash
./bin/dqmpctl ping 127.0.0.1:4242
./bin/dqmpctl ping 127.0.0.1:4243
```

4- testes statut et peers sur les nodes 1 et 2
```bash
./bin/dqmpctl --target http://127.0.0.1:8003 status
./bin/dqmpctl -t http://127.0.0.1:8003 peers
```

Tester les commandes dqmpctl data ... sur le Nœud 1 (API :8003) :
- Lister: `./bin/dqmpctl -t http://127.0.0.1:8003 list`
- Ajouter une valeur simple: `./bin/dqmpctl -t http://127.0.0.1:8003 put mykey "Hello DQMP Data!"`
- Ajouter une autre clé: `./bin/dqmpctl -t http://127.0.0.1:8003 put anotherkey "Some other value"`
- Récupérer la première clé (vers stdout): `./bin/dqmpctl -t http://127.0.0.1:8003 get mykey`
- Récupérer la deuxième clé (vers un fichier): 
```bash
./bin/dqmpctl -t http://127.0.0.1:8003 get anotherkey -o output.txt
cat output.txt
# Devrait afficher "Some other value"
rm output.txt
```
- Ajouter une valeur depuis un fichier: 
```bash
echo "Data from file" > input.txt
./bin/dqmpctl -t http://127.0.0.1:8003 put filekey -i input.txt
rm input.txt
./bin/dqmpctl -t http://127.0.0.1:8003 get filekey
# Devrait afficher "Data from file"
```
- Supprimer une clé: `./bin/dqmpctl -t http://127.0.0.1:8003 delete anotherkey`
- Tenter de récupérer la clé supprimée (devrait échouer) : `./bin/dqmpctl -t http://127.0.0.1:8003 get anotherkey`
- Vérifier le Nœud 2: Les opérations sur le Nœud 1 ne devraient PAS affecter le Nœud 2.
`./bin/dqmpctl -t http://127.0.0.1:8004 list`
- Lister: `./bin/dqmpctl -t http://127.0.0.1:8003 list`

## Netoyer 
`rm -rf dqmp_node_data* dqmp_identity*`