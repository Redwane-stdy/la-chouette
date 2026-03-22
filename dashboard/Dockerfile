FROM nginx:alpine

# -----------------------------------------------------------------------
# Copier les fichiers statiques du dashboard dans le répertoire servi
# par Nginx (image de base nginx:alpine)
# -----------------------------------------------------------------------
COPY index.html style.css app.js /usr/share/nginx/html/

# -----------------------------------------------------------------------
# Remplacer la configuration Nginx par défaut par la nôtre
# default.conf est la convention nginx:alpine pour le vhost principal
# -----------------------------------------------------------------------
COPY nginx.conf /etc/nginx/conf.d/default.conf

# -----------------------------------------------------------------------
# Exposer le port 80 — mappé dans docker-compose vers un port hôte
# -----------------------------------------------------------------------
EXPOSE 80
