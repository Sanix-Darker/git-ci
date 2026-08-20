FROM caddy:2-alpine

WORKDIR /srv/git-ci
COPY --chown=caddy:caddy site/ .

EXPOSE 8087

ENTRYPOINT ["caddy"]
CMD ["file-server", "--listen", ":8087", "--root", "."]
