#!/bin/sh

set +e

gcloud functions deploy tfe-to-teams \
    --entry-point=F \
    --memory=128MB \
    --region=us-central1 \
    --runtime=go122 \
    --env-vars-file=.env.yaml \
    --trigger-http \
    --timeout=10s
