FROM quay.io/operator-framework/opm:latest
COPY catalog/index.rendered.yaml /configs/index.yaml
EXPOSE 50051
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs"]
