# backlog
> go-tk: les briques que chaque service réécrit, extraites une fois

- json-envelope [transport] — un service répond à chaque route sous une forme de corps unique, erreurs comprises, et lit un corps de requête borné à une taille qu'il nomme, sans écrire l'encodage lui-même
- route-gate [transport] — un service ferme ses routes par défaut en ne nommant que les ouvertes, et une requête sur une route que personne n'a nommée est refusée quoi qu'elle demande  after: json-envelope
- bearer-principal [transport] — un service résout le porteur d'une requête couverte à travers un résolveur à lui, et ses handlers lisent l'appelant dans le contexte de la requête  after: route-gate
- capability-gate [transport] — un service nomme, par route, le droit qu'un appelant doit détenir, et un appelant qui ne le détient nulle part est refusé sans que le refus dise lequel  after: bearer-principal
- browser-origins [transport] — un service autorise une liste d'origines lue au démarrage, répond lui-même au preflight et refuse une origine que personne n'a listée
- call-rate [transport] — un service refuse un appelant au-delà de sa cadence en lui disant quand revenir, pendant qu'un appelant dans la cadence est servi  after: json-envelope
- password-hash [crypto] — un service hache et vérifie un mot de passe derrière une interface unique, le plaintext ne sortant jamais du type qui le porte, sur une implémentation choisie au câblage
- work-slots [crypto] — un service borne combien d'opérations coûteuses tournent en même temps, et un appelant qui ne trouve pas de place est refusé aussitôt au lieu d'attendre  after: password-hash
- opaque-token [crypto] — un service émet un porteur que la ligne qui le stocke ne permet pas de rejouer, le reparse à l'arrivée et ne l'imprime nulle part
- sortable-id [core] — un service émet des identifiants qui trient dans l'ordre de création, les reparse sous une seule forme canonique et refuse toutes les autres
- env-values [core] — un service lit un entier borné et une liste séparée par des virgules dans l'environnement, chaque valeur malformée nommée par sa clé au démarrage
- sql-failures [storage] — un service lit une collision d'index unique et une ligne absente comme des issues à lui, toute autre panne du driver arrivant sous un seul type portant sa cause, chaque instruction bornée par un délai
- keyset-pages [storage] — un service sert une liste page par page, la plus récente d'abord, en repartant du curseur où la page précédente s'est arrêtée, taille et curseur refusés à la frontière
