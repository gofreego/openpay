FROM alpine:latest

# Set your working directory (optional)
WORKDIR /app

# Install any additional packages (replace with your needs)
COPY application .
COPY dev.yaml .
COPY api/docs /app/api/docs
RUN chmod +x application

# Expose the ports the application uses
EXPOSE 8085 8086

# Define the command to run your application
CMD [ "/app/application" ]