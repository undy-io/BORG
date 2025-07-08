#!/usr/bin/env python3
"""
Setup script for Borg - OpenAI loadbalancing proxy
"""

from setuptools import setup, find_packages
from pathlib import Path

# Read the README file
this_directory = Path(__file__).parent
try:
    long_description = (this_directory / "README.md").read_text(encoding='utf-8')
except FileNotFoundError:
    long_description = "OpenAI loadbalancing proxy"

# Production dependencies (matches pyproject.toml [tool.poetry.dependencies])
install_requires = [
    "fastapi>=0.115.6",
    "uvicorn>=0.32.1",
    "httpx>=0.28.1",
    "kubernetes>=31.0.0",
    "pydantic>=2.10.3",
    "aiohttp>=3.12.13",
    "cryptography>=45.0.5"
]

# Development dependencies (matches pyproject.toml [tool.poetry.group.dev.dependencies])
dev_requires = [
    "pytest>=8.3.4",
    "mypy>=1.13.0",
    "black>=24.10.0",
    "isort>=5.13.2",
    "flake8>=7.1.1",
    "pytest-asyncio>=1.0.0",
]

setup(
    name="borg",
    version="0.1.0",
    author="Michael C. McMinn",
    author_email="mmcminn@uniondynamic.com",
    description="OpenAI loadbalancing proxy.",
    long_description=long_description,
    long_description_content_type="text/markdown",
    license="MIT",
    
    # Package discovery - matches pyproject.toml packages config
    packages=find_packages(where="src"),
    package_dir={"": "src"},
    
    # Python version requirement - matches pyproject.toml
    python_requires=">=3.12",
    
    # Production dependencies
    install_requires=install_requires,
    
    # Optional dependencies
    extras_require={
        "dev": dev_requires,
        "test": [
            "pytest>=8.3.4",
            "pytest-asyncio>=1.0.0",
        ],
        "lint": [
            "mypy>=1.13.0",
            "black>=24.10.0",
            "isort>=5.13.2",
            "flake8>=7.1.1",
        ],
    },
    
    # Entry points (uncomment if you add CLI commands)
    # entry_points={
    #     "console_scripts": [
    #         "borg=borg.cli:main",
    #     ],
    # },
    
    # Include package data
    include_package_data=True,
    
    # Classifiers for PyPI
    classifiers=[
        "Development Status :: 3 - Alpha",
        "Intended Audience :: Developers",
        "License :: OSI Approved :: MIT License",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.12",
        "Topic :: Software Development :: Libraries :: Python Modules",
        "Topic :: System :: Clustering",
        "Topic :: Internet :: WWW/HTTP :: HTTP Servers",
        "Topic :: Scientific/Engineering :: Artificial Intelligence",
    ],
    
    # Keywords
    keywords="kubernetes, load-balancing, ai, llm, vllm, openai, proxy, fastapi",
)